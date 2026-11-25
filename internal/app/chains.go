package app

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// Proxy chains (docs/chain-proxy.md §3.4): the list is stored like the
// manual nodes and composed by profile (chains.go there). What the
// service adds is keeping the user's references when the list changes:
//
//   - a rename rewrites the custom rules that target the old name and the
//     recorded group selections (selections.go), so after the reload the
//     groups select the chain again under its new name;
//   - a group that had a chain selected which is then deleted or disabled
//     falls back in the core to its first member; it is put back to the
//     member it had before the chain, when this run knows it;
//   - deleting a chain a custom rule targets is refused: the rule would
//     no longer load. Disabling keeps the name (a REJECT placeholder).

var chainIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ProxyChains returns the proxy chains in order.
func (s *Service) ProxyChains() []model.ProxyChain {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := model.CloneProxyChains(s.state.chains)
	if out == nil {
		out = []model.ProxyChain{}
	}
	return out
}

// chainFallback is a group that had a chain selected which goes away.
type chainFallback struct {
	group, chain, prev string
}

// SetProxyChains replaces the chain list and applies it (see the top).
func (s *Service) SetProxyChains(in []model.ProxyChain) ([]model.ProxyChain, error) {
	next := make([]model.ProxyChain, 0, len(in))
	names, ids := map[string]bool{}, map[string]bool{}
	for i, ch := range in {
		label := strings.TrimSpace(ch.Name)
		if label == "" {
			label = fmt.Sprintf(i18n.T("第 %d 条"), i+1)
		}
		ch, err := profile.NormalizeProxyChain(ch)
		if err != nil {
			return nil, fmt.Errorf(i18n.T("链路「%s」：%v"), label, err)
		}
		ch.ID = strings.TrimSpace(ch.ID)
		if ch.ID != "" && !chainIDPattern.MatchString(ch.ID) {
			return nil, fmt.Errorf(i18n.T("链路「%s」的 ID 只能由字母、数字、- 和 _ 组成"), ch.Name)
		}
		if names[ch.Name] {
			return nil, fmt.Errorf(i18n.T("已有名为「%s」的链路，请换一个名称。"), ch.Name)
		}
		if ch.ID != "" && ids[ch.ID] {
			return nil, fmt.Errorf(i18n.T("链路 ID「%s」重复"), ch.ID)
		}
		if ch.Name == profile.ChainGroupName || ch.Name == profile.ChainGroupAltName {
			return nil, fmt.Errorf(i18n.T("「%s」是链路总组的名字，请换一个名称。"), ch.Name)
		}
		names[ch.Name], ids[ch.ID] = true, true
		next = append(next, ch)
	}

	s.opMu.Lock()
	defer s.opMu.Unlock()

	old := s.d.store.ProxyChains()
	oldByID := map[string]model.ProxyChain{}
	oldNames := map[string]bool{}
	for _, ch := range old {
		oldByID[ch.ID] = ch
		oldNames[ch.Name] = true
	}
	s.mu.Lock()
	snap := s.state.proxies
	bin := s.state.coreBin
	s.mu.Unlock()
	for _, ch := range next {
		if _, taken := snap.Proxies[ch.Name]; taken && !oldNames[ch.Name] {
			return nil, fmt.Errorf(i18n.T("「%s」与当前配置中的节点或代理组重名，请换一个名称。"), ch.Name)
		}
	}

	renames := map[string]string{} // old name → new name
	gone := map[string]bool{}      // enabled before, not any more
	nextByID := map[string]model.ProxyChain{}
	for _, ch := range next {
		if ch.ID != "" {
			nextByID[ch.ID] = ch
		}
	}
	for _, o := range old {
		n, kept := nextByID[o.ID]
		if kept && n.Name != o.Name {
			renames[o.Name] = n.Name
		}
		if o.Enabled && (!kept || !n.Enabled) {
			gone[o.Name] = true
			if kept {
				gone[n.Name] = true
			}
		}
	}

	// custom rules follow renames; a deleted chain must not be a target
	oldRules := s.d.store.CustomRules()
	rules := make([]model.CustomRule, len(oldRules))
	copy(rules, oldRules)
	rulesChanged := false
	for i, r := range rules {
		if to, ok := renames[r.Target]; ok {
			rules[i].Target = to
			rulesChanged = true
			continue
		}
		if oldNames[r.Target] && !names[r.Target] {
			return nil, fmt.Errorf(i18n.T("链路「%s」被自定义规则「%s,%s」使用，请先修改这条规则，或改为停用链路。"), r.Target, r.Type, r.Payload)
		}
	}

	// groups that will lose their selected chain
	var fallbacks []chainFallback
	for _, g := range snap.Groups {
		if gone[g.Now] && g.Name != probeGroup {
			fallbacks = append(fallbacks, chainFallback{group: g.Name, chain: g.Now, prev: s.previousSelection(g.Name)})
		}
	}

	var p prepared
	if bin != "" {
		var err error
		p, err = s.prepare(composeArgs{chains: next, useChains: true, custom: rules, useRules: true})
		if err != nil && !errors.Is(err, errNoSubscription) {
			return nil, err
		}
	}

	saved, err := s.d.store.SetProxyChains(next)
	if err != nil {
		p.discard()
		return nil, fmt.Errorf(i18n.T("保存链式代理失败：%v"), err)
	}
	undo := func() {
		_, _ = s.d.store.SetProxyChains(old)
		if rulesChanged {
			_, _ = s.d.store.SetCustomRules(oldRules)
		}
		back := map[string]string{}
		for from, to := range renames {
			back[to] = from
		}
		s.renameSelections(back)
		s.refreshStore(TopicProxies, TopicRules)
	}
	if rulesChanged {
		if _, err := s.d.store.SetCustomRules(rules); err != nil {
			p.discard()
			undo()
			return nil, fmt.Errorf(i18n.T("保存自定义规则失败：%v"), err)
		}
	}
	s.renameSelections(renames)
	s.refreshStore(TopicProxies, TopicRules)

	if !s.running() {
		p.discard()
		return saved, nil
	}
	if p.tmp != "" {
		err = s.reloadPrepared(p)
	} else {
		err = s.reloadLocked()
	}
	if err != nil {
		undo()
		return nil, fmt.Errorf(i18n.T("链式代理已撤销：%v"), err)
	}
	if rulesChanged {
		s.refreshRules()
	}
	s.restoreChainFallbacks(fallbacks)
	return saved, nil
}

// restoreChainFallbacks puts the groups that had a removed chain selected
// back to their previous member (when known and still there).
func (s *Service) restoreChainFallbacks(fbs []chainFallback) {
	if len(fbs) == 0 {
		return
	}
	groups := map[string]model.ProxyGroup{}
	for _, g := range s.Proxies().Groups {
		groups[g.Name] = g
	}
	var back []string
	for _, fb := range fbs {
		g, ok := groups[fb.group]
		if !ok || g.Now == fb.chain || fb.prev == "" || fb.prev == g.Now || !slices.Contains(g.All, fb.prev) {
			continue
		}
		ctx, cancel := s.svcCtx(ctrlTimeout)
		err := s.d.ctrl.SelectProxy(ctx, fb.group, fb.prev)
		cancel()
		if err != nil {
			s.alog.Warnf("chains: reselect %s -> %s: %v", fb.group, fb.prev, err)
			continue
		}
		s.rememberSelection(fb.group, fb.prev)
		back = append(back, fmt.Sprintf(i18n.T("「%s」改回「%s」"), fb.group, fb.prev))
	}
	if len(back) > 0 {
		s.refreshProxies()
		s.notify(Notice{Kind: "toast", Title: i18n.T("链路已移除，相关代理组已改回之前的选择"), Detail: strings.Join(back, i18n.T("、"))})
	}
}

// noteChainWarnings keeps what a committed composition said of the
// chains and tells the user when that changes (a chain that stopped
// validating after a subscription update, for one).
func (s *Service) noteChainWarnings(all []string) {
	var warns []string
	for _, w := range all {
		if strings.HasPrefix(w, profile.ChainWarningPrefix) {
			warns = append(warns, strings.TrimPrefix(w, profile.ChainWarningPrefix))
		}
	}
	s.mu.Lock()
	same := slices.Equal(s.state.chainWarns, warns)
	s.state.chainWarns = warns
	s.mu.Unlock()
	if same || len(warns) == 0 {
		return
	}
	s.notify(Notice{Kind: "toast", Title: i18n.T("链式代理有需要注意的地方"), Detail: strings.Join(warns, "\n")})
}
