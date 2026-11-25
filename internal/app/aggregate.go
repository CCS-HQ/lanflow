package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// Aggregate subscriptions (docs/aggregate.md): a subscription whose
// content Lanflow builds itself from several sources. The spec is stored
// next to the content (profile.Store.AggregateSpec); updating rebuilds it.

// AggregateSpec returns how the aggregate subscription id is built.
func (s *Service) AggregateSpec(id string) (model.AggregateSpec, error) {
	sub, ok := s.d.store.Get(id)
	if !ok {
		return model.AggregateSpec{}, errors.New(i18n.T("订阅不存在，可能已被删除。"))
	}
	if sub.Source != model.SourceAggregate {
		return model.AggregateSpec{}, errors.New(i18n.T("这不是聚合订阅。"))
	}
	spec, ok := s.d.store.AggregateSpec(id)
	if !ok {
		return model.AggregateSpec{}, errors.New(i18n.T("聚合订阅的设置丢失了，请重新添加来源。"))
	}
	return spec, nil
}

// PreviewAggregate downloads the sources and builds the profile without
// saving anything; sub supplies the download options (User-Agent, via
// proxy, timeout) and, for an existing subscription, its ID.
func (s *Service) PreviewAggregate(sub model.Subscription, spec model.AggregateSpec) (model.AggregatePreview, error) {
	spec = withSourceIDs(spec)
	if err := profile.ValidateAggregateSpec(spec, i18n.Tf); err != nil {
		return model.AggregatePreview{}, err
	}
	pv, _, err := profile.BuildAggregate(spec, s.gatherAggregate(sub, spec), i18n.Tf)
	if err != nil {
		return pv, err
	}
	return pv, nil
}

// SaveAggregate creates (empty ID) or changes an aggregate subscription:
// the sources are downloaded, the profile built and checked with the core,
// and only then are the spec, the content and the record stored. A new
// one is switched to when use is set (or when it is the first
// subscription); a changed current one is reloaded.
func (s *Service) SaveAggregate(in model.Subscription, spec model.AggregateSpec, use bool) (sub model.Subscription, err error) {
	defer func() { s.logOp("save aggregate subscription "+strings.TrimSpace(in.Name), err) }()
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return sub, errors.New(i18n.T("请填写订阅名称。"))
	}
	if in.TimeoutSec < 0 || time.Duration(in.TimeoutSec)*time.Second > maxSubTimeout {
		return sub, fmt.Errorf(i18n.T("下载超时须为 1–%d 秒之间的数字。"), int(maxSubTimeout/time.Second))
	}
	spec = withSourceIDs(spec)
	if err := profile.ValidateAggregateSpec(spec, i18n.Tf); err != nil {
		return sub, err
	}
	if in.ID != "" {
		old, ok := s.d.store.Get(in.ID)
		if !ok {
			return sub, errors.New(i18n.T("订阅不存在，可能已被删除。"))
		}
		if old.Source != model.SourceAggregate {
			return sub, errors.New(i18n.T("这不是聚合订阅。"))
		}
	}
	pv, data, err := profile.BuildAggregate(spec, s.gatherAggregate(in, spec), i18n.Tf)
	if err != nil {
		return sub, err
	}
	now := s.d.now()
	fill := func(cur *model.Subscription) {
		cur.Name = name
		cur.AutoUpdateHours = snapAutoUpdate(in.AutoUpdateHours)
		cur.UserAgent, cur.ViaProxy, cur.TimeoutSec = in.UserAgent, in.ViaProxy, in.TimeoutSec
		applyAggregateReport(cur, pv, now)
	}

	if in.ID == "" {
		sub = model.Subscription{Source: model.SourceAggregate}
		fill(&sub)
		if sub, err = s.d.store.Put(sub); err != nil {
			return model.Subscription{}, fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
		}
		if err := s.d.store.SetAggregateSpec(sub.ID, spec); err != nil {
			_ = s.d.store.Delete(sub.ID)
			return model.Subscription{}, fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
		}
		if err := s.d.store.SetContent(sub.ID, data); err != nil {
			_ = s.d.store.Delete(sub.ID)
			return model.Subscription{}, fmt.Errorf(i18n.T("保存订阅内容失败：%v"), err)
		}
		return s.finishImport(sub, use)
	}

	id := in.ID
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, ok := s.d.store.Get(id); !ok {
		return sub, errors.New(i18n.T("订阅不存在，可能已被删除。"))
	}
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	var p prepared
	if bin != "" {
		if p, err = s.prepare(composeArgs{subID: id, content: data}); err != nil {
			return sub, fmt.Errorf(i18n.T("生成的配置未通过校验，聚合订阅没有保存：%v"), err)
		}
	}
	oldSpec, hadSpec := s.d.store.AggregateSpec(id)
	if err := s.d.store.SetAggregateSpec(id, spec); err != nil {
		p.discard()
		return sub, fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
	}
	if err := s.d.store.SetContent(id, data); err != nil {
		p.discard()
		if hadSpec {
			_ = s.d.store.SetAggregateSpec(id, oldSpec)
		}
		return sub, fmt.Errorf(i18n.T("保存订阅内容失败：%v"), err)
	}
	if sub, err = s.mergeSub(id, fill); err != nil {
		p.discard()
		return sub, fmt.Errorf(i18n.T("保存订阅失败：%v"), err)
	}
	s.refreshStore(TopicSubs)
	if cur, ok := s.d.store.Current(); !ok || cur.ID != id {
		p.discard()
		return sub, nil
	}
	if p.tmp != "" {
		err = s.reloadPrepared(p)
	} else {
		err = s.reloadLocked()
	}
	return sub, err
}

// buildAggregate rebuilds a stored aggregate subscription (updateOne).
func (s *Service) buildAggregate(sub model.Subscription) ([]byte, model.AggregatePreview, error) {
	spec, ok := s.d.store.AggregateSpec(sub.ID)
	if !ok {
		return nil, model.AggregatePreview{}, errors.New(i18n.T("聚合订阅的设置丢失了，请重新添加来源。"))
	}
	pv, data, err := profile.BuildAggregate(spec, s.gatherAggregate(sub, spec), i18n.Tf)
	if err != nil {
		return nil, pv, err
	}
	return data, pv, nil
}

// applyAggregateReport stores what a build found out: the summed traffic
// of the link sources, the earliest expiry, and the sources that failed.
func applyAggregateReport(sub *model.Subscription, pv model.AggregatePreview, now time.Time) {
	sub.Upload, sub.Download, sub.Total, sub.Expire = 0, 0, 0, time.Time{}
	for _, r := range pv.Sources {
		sub.Upload += r.Upload
		sub.Download += r.Download
		sub.Total += r.Total
		if !r.Expire.IsZero() && (sub.Expire.IsZero() || r.Expire.Before(sub.Expire)) {
			sub.Expire = r.Expire
		}
	}
	sub.UpdatedAt = now
	sub.LastError = aggregateProblems(pv)
}

// aggregateProblems describes the sources that failed in a build that
// still produced a profile, "" when none did.
func aggregateProblems(pv model.AggregatePreview) string {
	var failed []string
	for _, r := range pv.Sources {
		if r.Error != "" {
			failed = append(failed, r.Error)
		}
	}
	if len(failed) == 0 {
		return ""
	}
	return fmt.Sprintf(i18n.T("已更新，但有 %d 个来源失败：%s"), len(failed), strings.Join(failed, i18n.T("；")))
}

// gatherAggregate collects the content of the enabled sources of spec,
// downloading the links in parallel with sub's download options.
func (s *Service) gatherAggregate(sub model.Subscription, spec model.AggregateSpec) []profile.AggregateInput {
	var out []profile.AggregateInput
	for _, src := range spec.Sources {
		if !src.Disabled {
			out = append(out, profile.AggregateInput{Source: src})
		}
	}
	timeout := subTimeout(sub)
	var wg sync.WaitGroup
	for i := range out {
		in := &out[i]
		switch in.Source.Kind {
		case model.AggSourceLinks:
			in.Data = []byte(in.Source.Links)
		case model.AggSourceSub:
			ref, ok := s.d.store.Get(in.Source.SubID)
			switch {
			case !ok:
				in.Err = errors.New(i18n.T("引用的订阅已被删除"))
			case ref.Source == model.SourceAggregate:
				in.Err = errors.New(i18n.T("不能引用另一个聚合订阅"))
			default:
				data, err := s.d.store.Content(ref.ID)
				if err != nil {
					in.Err = fmt.Errorf(i18n.T("读取「%s」失败：%v"), ref.Name, err)
				}
				in.Data = data
			}
		case model.AggSourceURL:
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := s.svcCtx(timeout)
				defer cancel()
				f, err := s.d.prof.FetchRaw(ctx, s.fetchClient(sub.ViaProxy, timeout), strings.TrimSpace(in.Source.URL), sub.UserAgent)
				if err != nil {
					in.Err = err // already says 下载失败 / 订阅链接无效
					return
				}
				in.Data, in.Fetched = f.Data, f
			}()
		}
	}
	wg.Wait()
	return out
}

// withSourceIDs gives every source without an ID one.
func withSourceIDs(spec model.AggregateSpec) model.AggregateSpec {
	spec = spec.Clone()
	for i := range spec.Sources {
		if spec.Sources[i].ID == "" {
			var b [4]byte
			_, _ = rand.Read(b[:])
			spec.Sources[i].ID = hex.EncodeToString(b[:])
		}
	}
	return spec
}
