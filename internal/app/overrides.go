package app

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// The ordered override list (model.Override, docs/design.md §6.9). Every
// change goes through opMu like a subscription switch: the composed config
// of a subscription the entry takes part in is validated with the core
// (mihomo -t) before the store changes, and the running core reloads when
// the current subscription is affected.

var errOverrideMissing = i18n.Error("覆写不存在，可能已被删除。")

// hasID reports whether ids holds id.
func hasID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// OverrideList returns the override list in order.
func (s *Service) OverrideList() []model.Override {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.Override(nil), s.state.overrides.List...)
}

// overrideTakesPart reports whether o takes part in sub's config.
func overrideTakesPart(o model.Override, sub model.Subscription) bool {
	return o.Global || hasID(sub.Overrides, o.ID)
}

// affectsCurrent reports whether a change of o changes the current
// subscription's config.
func (s *Service) affectsCurrent(o model.Override) bool {
	cur, ok := s.d.store.Current()
	return ok && overrideTakesPart(o, cur)
}

// validateOverrideLocked checks o: the composed config of the subscription
// args names (else the current one when o takes part in it, else the
// first one it takes part in) passes the core's validation; when no
// subscription uses o, only its YAML syntax is checked. opMu must be held.
func (s *Service) validateOverrideLocked(o model.Override, args composeArgs) error {
	syntax := func() error {
		if strings.TrimSpace(o.Text) == "" {
			return nil
		}
		if _, err := profile.ApplyOverride([]byte("{}\n"), o.Text); err != nil {
			return fmt.Errorf(i18n.T("覆写未保存，%v"), err)
		}
		return nil
	}
	if !o.Enabled {
		return syntax()
	}
	target := args.subID
	if target == "" {
		if cur, ok := s.d.store.Current(); ok && overrideTakesPart(o, cur) {
			target = cur.ID
		} else {
			for _, sub := range s.d.store.List() {
				if overrideTakesPart(o, sub) {
					target = sub.ID
					break
				}
			}
		}
	}
	if target == "" {
		return syntax()
	}
	args.subID = target
	args.ov = &o
	p, err := s.prepare(args)
	if err != nil {
		return fmt.Errorf(i18n.T("覆写未保存，%v"), err)
	}
	p.discard() // validated only; the reload composes again
	return nil
}

// applyOverrideChangeLocked refreshes the caches after the store changed
// and reloads the core when the current subscription's config changed.
// opMu must be held.
func (s *Service) applyOverrideChangeLocked(o model.Override) error {
	affects := s.affectsCurrent(o)
	s.refreshStore(TopicSettings)
	if affects {
		return s.reloadLocked()
	}
	return nil
}

// fetchOverride downloads a URL override's text.
func (s *Service) fetchOverride(rawURL string) (string, error) {
	ctx, cancel := s.ctrlCtx(nil, fetchTimeout)
	defer cancel()
	text, err := profile.FetchOverride(ctx, s.fetchClient(true, 0), rawURL)
	if err != nil {
		return "", fmt.Errorf(i18n.T("下载覆写失败：%v"), err)
	}
	return text, nil
}

// SaveOverrideEntry adds or updates an override entry (API).
func (s *Service) SaveOverrideEntry(in model.Override) (out model.Override, err error) {
	defer func() { s.logOp("save override entry "+in.Name, err) }()
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, errors.New(i18n.T("请填写覆写名称。"))
	}
	if in.Source == model.OverrideURL {
		in.URL = strings.TrimSpace(in.URL)
		if u, err := url.Parse(in.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return in, errors.New(i18n.T("覆写链接无效。请输入以 http:// 或 https:// 开头的完整链接。"))
		}
	} else {
		in.Source, in.URL = model.OverrideLocal, ""
	}
	// lookup finds the entry in being replaced. Only the legacy IDs may be
	// created by name: "global" and a subscription's ID (settings import).
	lookup := func() (model.Override, error) {
		if in.ID == "" {
			return model.Override{}, nil
		}
		old, ok := s.d.store.GetOverride(in.ID)
		if !ok {
			if _, isSub := s.d.store.Get(in.ID); in.ID != "global" && !isSub {
				return old, errOverrideMissing
			}
		}
		return old, nil
	}
	if in.Source == model.OverrideURL && strings.TrimSpace(in.Text) == "" {
		// Download before opMu is taken: it may take fetchTimeout, and
		// opMu holds up every start, stop, switch and settings change.
		if _, err := lookup(); err != nil {
			return in, err
		}
		text, err := s.fetchOverride(in.URL)
		if err != nil {
			return in, err
		}
		in.Text = text
		in.UpdatedAt = s.d.now()
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	prev, err := lookup() // again under the lock: it may have gone meanwhile
	if err != nil {
		return in, err
	}
	if in.ID == "global" {
		in.Global = true
	}
	if in.ID == "" || in.Text != prev.Text || in.UpdatedAt.IsZero() {
		in.UpdatedAt = s.d.now()
	}
	if err := s.validateOverrideLocked(in, composeArgs{}); err != nil {
		return in, err
	}
	out, err = s.d.store.PutOverride(in)
	if err != nil {
		return in, fmt.Errorf(i18n.T("保存覆写失败：%v"), err)
	}
	// Both the old and the new form may touch the current config (e.g. a
	// global entry made local).
	if prev.ID != "" && s.affectsCurrent(prev) {
		s.refreshStore(TopicSettings)
		return out, s.reloadLocked()
	}
	return out, s.applyOverrideChangeLocked(out)
}

// DeleteOverrideEntry removes an override entry (API).
func (s *Service) DeleteOverrideEntry(id string) (err error) {
	defer func() { s.logOp("delete override "+id, err) }()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.deleteOverrideLocked(id)
}

func (s *Service) deleteOverrideLocked(id string) error {
	o, ok := s.d.store.GetOverride(id)
	if !ok {
		return nil
	}
	affects := s.affectsCurrent(o)
	if err := s.d.store.DeleteOverride(id); err != nil {
		return fmt.Errorf(i18n.T("删除覆写失败：%v"), err)
	}
	s.refreshStore(TopicSettings, TopicSubs)
	if affects {
		return s.reloadLocked()
	}
	return nil
}

// MoveOverrideEntry moves an entry by delta positions (API).
func (s *Service) MoveOverrideEntry(id string, delta int) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	o, ok := s.d.store.GetOverride(id)
	if !ok {
		return errOverrideMissing
	}
	if err := s.d.store.MoveOverride(id, delta); err != nil {
		return fmt.Errorf(i18n.T("调整覆写顺序失败：%v"), err)
	}
	if o.Enabled {
		return s.applyOverrideChangeLocked(o)
	}
	s.refreshStore(TopicSettings)
	return nil
}

// UpdateOverrideEntry re-downloads a URL entry (API).
func (s *Service) UpdateOverrideEntry(id string) (err error) {
	defer func() { s.logOp("update override "+id, err) }()
	was, ok := s.d.store.GetOverride(id)
	if !ok {
		return errOverrideMissing
	}
	if was.Source != model.OverrideURL || was.URL == "" {
		return errors.New(i18n.T("这条覆写不是从链接导入的，无法更新。"))
	}
	// Download without opMu (see SaveOverrideEntry), then take the entry
	// as it is now: edits made during the download stay.
	text, err := s.fetchOverride(was.URL)
	if err != nil {
		return err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	o, ok := s.d.store.GetOverride(id)
	if !ok {
		return errOverrideMissing
	}
	if o.Source != model.OverrideURL || o.URL != was.URL {
		return errors.New(i18n.T("这条覆写在下载期间被修改了，请重新更新。"))
	}
	o.Text = text
	o.UpdatedAt = s.d.now()
	if err := s.validateOverrideLocked(o, composeArgs{}); err != nil {
		return fmt.Errorf(i18n.T("覆写未更新：%v"), err)
	}
	if _, err := s.d.store.PutOverride(o); err != nil {
		return fmt.Errorf(i18n.T("保存覆写失败：%v"), err)
	}
	return s.applyOverrideChangeLocked(o)
}

// overridesSnapshot builds the Overrides read from the store: the list,
// and the legacy views of its "global" and per-subscription entries.
func overridesSnapshot(list []model.Override, subs []model.Subscription) model.Overrides {
	ov := model.Overrides{PerSub: map[string]string{}, List: append([]model.Override(nil), list...)}
	ids := map[string]bool{}
	for _, sub := range subs {
		ids[sub.ID] = true
	}
	for _, o := range list {
		switch {
		case o.ID == "global":
			ov.Global = o.Text
		case ids[o.ID] && o.Text != "":
			ov.PerSub[o.ID] = o.Text
		}
	}
	return ov
}
