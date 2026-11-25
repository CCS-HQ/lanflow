package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/syncfile"
)

// Loading lanflow.yaml into the service (configuration sync and
// API.ApplyConfigFile). The document is declarative: for the parts it
// covers — settings without the machine-local items, the override list,
// the manual nodes (kept when the file has no manualNodes key),
// the proxy chains (kept when the file has no proxyChains key),
// the custom rules and the URL subscriptions — this machine ends up with
// exactly what the file says. Subscriptions imported from local files
// stay, and so does the current subscription (never deleted from under
// the running core).
//
// Everything is checked before anything is stored: the file's own rules
// (syncfile.Decode), the custom rules, the ports, and the composed
// config of the current subscription with `mihomo -t`. Then the store is
// written and the settings go through SaveSettings, which applies them
// hot, reloads or restarts the core as each change needs.
//
// Loading is all or nothing. When a step after the checks fails (the
// disk, a port taken in the meantime, a reload the core refuses), what
// was already written is put back (restorePlan), so "not loaded" means
// this machine has exactly what it had. Subscriptions the file drops are
// deleted last, when nothing can fail any more: a deletion takes the
// downloaded content with it and could not be undone.
//
// Errors come in two kinds. A *docError from planning says the document
// itself is wrong, and the same content will never load. Everything from
// checkPlan on is wrapped in *retryError: it depends on this machine now
// (a port in use, an older core, a timeout, the disk) and the same
// content may load later, so configuration sync tries it again.

// docError is a problem with one part of the document; path locates it
// for the line number (syncfile.LineOf).
type docError struct {
	path string
	err  error
}

func (e *docError) Error() string { return e.path + i18n.T("：") + e.err.Error() }
func (e *docError) Unwrap() error { return e.err }

// retryError marks a failure to load a document that is not the
// document's fault: the same content may load later.
//
// limited marks a failure of composing or validating the configuration
// (checkPlan's `mihomo -t`). It may be this machine's (an older core, a
// timeout) or the document's own (an override that makes the config
// invalid), and nothing tells the two apart, so it is tried a few times
// and then left to the user rather than run every hour for good.
type retryError struct {
	err     error
	limited bool
}

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

// retryable reports whether err is worth trying the same content again,
// and how often at most (0 = as long as it fails).
func retryable(err error) (retry bool, limit int) {
	var re *retryError
	if !errors.As(err, &re) {
		return false, 0
	}
	if re.limited {
		return true, syncRetryLimited
	}
	return true, 0
}

type nodeSecretKey struct {
	id, field string
}

// applyPlan is what loading a document changes.
type applyPlan struct {
	cur, next             localState
	put                   []model.Subscription // subscriptions to add or update
	del                   []model.Subscription // URL subscriptions the file no longer has
	delNodes              []string             // manual node IDs the file no longer has
	clearedNodeSecrets    []nodeSecretKey      // manual node secret fields explicitly cleared in the file
	keptCur               string               // the current subscription, which the file drops but stays
	missing               []string             // names of subscriptions without a link on this machine
	missingNodes          []string             // names of manual nodes without credentials on this machine
	unresolvedRefs        map[string]string    // manualNodes.<id>.<field> → unresolved keychain:<key> ref
	replaceUnresolvedRefs bool
	fresh                 []string // IDs of new subscriptions to download
	changed               []string // "settings", "overrides", "customRules", "manualNodes", "proxyChains", "subscriptions"
	before                []byte   // lanflow.yaml of this machine now
	after                 []byte   // lanflow.yaml of this machine after loading
}

// planDoc works out what loading doc does, without changing anything.
func (s *Service) planDoc(doc syncfile.Doc) (*applyPlan, error) {
	cur := s.snapshotLocal()
	p := &applyPlan{cur: cur}
	next := localState{set: normalizeSettings(syncfile.KeepLocal(doc.Settings, cur.set))}

	// ---- subscriptions ----
	byID := map[string]model.Subscription{}
	for _, sub := range cur.subs {
		byID[sub.ID] = sub
	}
	inDoc := map[string]bool{}
	curID := ""
	if c, ok := s.d.store.Current(); ok {
		curID = c.ID
	}
	for i, e := range doc.Subscriptions {
		inDoc[e.ID] = true
		old, had := byID[e.ID]
		if had && old.Source != urlSubscriptionK {
			return nil, &docError{"subscriptions[" + strconv.Itoa(i) + "].id", errors.New(i18n.T("与本机一个从文件导入的订阅 ID 相同"))}
		}
		sub := old
		if !had {
			sub = model.Subscription{ID: e.ID, Source: urlSubscriptionK}
		}
		sub.Name = strings.TrimSpace(e.Name)
		sub.URL = s.resolveLink(e.URL, old.URL)
		sub.UserAgent, sub.ViaProxy, sub.TimeoutSec = e.UserAgent, e.ViaProxy, e.TimeoutSec
		sub.AutoUpdateHours = snapAutoUpdate(e.AutoUpdateHours)
		sub.Overrides = append([]string(nil), e.Overrides...)
		if sub.URL == "" {
			p.missing = append(p.missing, sub.Name)
		}
		if !had || !subEqual(old, sub) {
			p.put = append(p.put, sub)
		}
		if (!had || old.URL == "") && sub.URL != "" {
			// new, or its link just arrived (entered, or unsealed): download
			p.fresh = append(p.fresh, sub.ID)
		}
		next.subs = append(next.subs, sub)
	}
	for _, sub := range cur.subs {
		switch {
		case inDoc[sub.ID]:
		case sub.Source != urlSubscriptionK:
			next.subs = append(next.subs, sub) // local file subscriptions stay
		case sub.ID == curID:
			p.keptCur = sub.Name
			next.subs = append(next.subs, sub)
		default:
			p.del = append(p.del, sub)
		}
	}

	// ---- overrides: the file's list, then the entries of this machine's
	// own subscriptions that never travel (buildDoc) ----
	stays := map[string]bool{}
	for _, sub := range next.subs {
		if !inDoc[sub.ID] {
			stays[sub.ID] = true
		}
	}
	for _, o := range doc.Overrides {
		next.ovs = append(next.ovs, overrideFromDoc(o))
	}
	for _, o := range cur.ovs {
		if stays[o.ID] {
			next.ovs = append(next.ovs, o)
		}
	}
	// keep the download time of URL entries whose text did not change
	for i, o := range next.ovs {
		for _, old := range cur.ovs {
			if old.ID == o.ID && old.Text == o.Text {
				next.ovs[i].UpdatedAt = old.UpdatedAt
			}
		}
	}

	// ---- manual nodes: a file without the key keeps this machine's ----
	next.manualNodes = model.CloneManualNodes(cur.manualNodes)
	if doc.ManualNodes != nil {
		p.replaceUnresolvedRefs = true
		p.unresolvedRefs = map[string]string{}
		curNodes := map[string]model.ManualNode{}
		for _, n := range cur.manualNodes {
			curNodes[n.ID] = n
		}
		inNodes := map[string]bool{}
		next.manualNodes = make([]model.ManualNode, 0, len(doc.ManualNodes))
		for i, e := range doc.ManualNodes {
			inNodes[e.ID] = true
			old, had := curNodes[e.ID]
			n := manualNodeFromDoc(e)
			nodeMissing := false
			for _, f := range syncfile.ManualNodeSecretFields {
				ref := *e.SecretFieldPtr(f)
				localVal := ""
				if had {
					localVal = *modelSecretPtr(&old, f)
				}
				val, miss := s.resolveManualSecret(ref, localVal)
				*modelSecretPtr(&n, f) = val
				if miss {
					nodeMissing = true
					p.unresolvedRefs[syncfile.ManualNodeSecretPath(e.ID, f)] = ref
				} else if had && ref == "" && localVal != "" {
					p.clearedNodeSecrets = append(p.clearedNodeSecrets, nodeSecretKey{id: e.ID, field: f})
				}
			}
			var v model.ManualNode
			var err error
			if nodeMissing {
				v, err = profile.ValidateManualNodeAllowMissingSecret(n)
			} else {
				v, err = profile.ValidateManualNode(n)
			}
			if err != nil {
				return nil, &docError{"manualNodes[" + strconv.Itoa(i) + "]", err}
			}
			if nodeMissing {
				p.missingNodes = append(p.missingNodes, v.Name)
			}
			next.manualNodes = append(next.manualNodes, v)
		}
		for _, old := range cur.manualNodes {
			if !inNodes[old.ID] {
				p.delNodes = append(p.delNodes, old.ID)
			}
		}
	}

	// ---- proxy chains: a file without the key keeps this machine's ----
	next.chains = cur.chains
	if doc.ProxyChains != nil {
		next.chains = chainsFromDoc(doc.ProxyChains)
		for i, c := range next.chains {
			v, err := profile.NormalizeProxyChain(c)
			if err != nil {
				return nil, &docError{"proxyChains[" + strconv.Itoa(i) + "]", chainErrText(err)}
			}
			next.chains[i] = v
		}
	}

	// ---- custom rules ----
	for i, r := range doc.CustomRules {
		cr, err := validateCustomRule(model.CustomRule{Type: r.Type, Payload: r.Payload, Target: r.Target})
		if err != nil {
			return nil, &docError{"customRules[" + strconv.Itoa(i) + "]", err}
		}
		next.rules = append(next.rules, cr)
	}
	// keep local IDs of rules that did not change, so nothing churns
	for i := range next.rules {
		if i < len(cur.rules) && ruleKey(cur.rules[i]) == ruleKey(next.rules[i]) {
			next.rules[i].ID = cur.rules[i].ID
		}
	}

	p.next = next
	p.before = syncfile.Encode(buildDoc(cur, linksKeychain))
	p.after = syncfile.Encode(buildDoc(next, linksKeychain))
	section := func(st localState, part string) []byte {
		d := buildDoc(st, linksKeychain)
		switch part {
		case "settings":
			d.Overrides, d.CustomRules, d.Subscriptions, d.ManualNodes, d.ProxyChains = nil, nil, nil, nil, nil
		case "overrides":
			d = syncfile.Doc{Overrides: d.Overrides}
		case "customRules":
			d = syncfile.Doc{CustomRules: d.CustomRules}
		case "proxyChains":
			d = syncfile.Doc{ProxyChains: d.ProxyChains}
		}
		return syncfile.Encode(d)
	}
	for _, part := range []string{"settings", "overrides", "customRules"} {
		if !bytes.Equal(section(cur, part), section(next, part)) {
			p.changed = append(p.changed, part)
		}
	}
	// Compare full ManualNode models because linksKeychain masks credentials.
	if !manualNodesEqual(cur.manualNodes, next.manualNodes) {
		p.changed = append(p.changed, "manualNodes")
	}
	if !bytes.Equal(section(cur, "proxyChains"), section(next, "proxyChains")) {
		p.changed = append(p.changed, "proxyChains")
	}
	if len(p.put) > 0 || len(p.del) > 0 {
		p.changed = append(p.changed, "subscriptions")
	}
	return p, nil
}

func ruleKey(r model.CustomRule) string { return r.Type + "," + r.Payload + "," + r.Target }

// subEqual compares what the file sets of a subscription.
func subEqual(a, b model.Subscription) bool {
	return a.Name == b.Name && a.URL == b.URL && a.UserAgent == b.UserAgent && a.ViaProxy == b.ViaProxy &&
		a.AutoUpdateHours == b.AutoUpdateHours && a.TimeoutSec == b.TimeoutSec && equalStrings(a.Overrides, b.Overrides)
}

// applyDoc loads doc (a dry run only plans). The returned result has
// the diff of this machine's lanflow.yaml; on error nothing is stored
// unless the error says it came after the store was written.
func (s *Service) applyDoc(doc syncfile.Doc, dryRun bool) (model.ConfigApplyResult, error) {
	p, err := s.planDoc(doc)
	if err != nil {
		return model.ConfigApplyResult{}, err
	}
	res := model.ConfigApplyResult{Changed: p.changed, Diff: s.d.prof.Diff(p.before, p.after)}
	if len(p.changed) == 0 {
		res.Diff = nil
		res.Summary = i18n.T("文件与本机配置一致，没有需要加载的内容。")
		return res, nil
	}
	res.Summary = p.summary()
	if dryRun {
		return res, nil
	}
	if err := s.checkPlan(p); err != nil {
		var de *docError
		return res, &retryError{err: err, limited: !errors.As(err, &de)}
	}
	if err := s.storePlan(p); err != nil {
		if rerr := s.restorePlan(p); rerr != nil {
			s.alog.Errorf("config load: undo after a failed save: %v", rerr)
			return res, &retryError{err: fmt.Errorf(i18n.T("保存配置失败，可能只加载了一部分：%v"), err)}
		}
		return res, &retryError{err: fmt.Errorf(i18n.T("保存配置失败，已恢复原有配置：%v"), err)}
	}
	warn, err := s.applyPlanSettings(p)
	if err != nil {
		if rerr := s.restorePlan(p); rerr != nil {
			s.alog.Errorf("config load: undo after a failed apply: %v", rerr)
			err = fmt.Errorf(i18n.T("%w（恢复原有配置时又出错，可能只加载了一部分：%v）"), err, rerr)
		}
		return res, &retryError{err: err}
	}
	// Nothing can fail the load any more: drop what the file no longer has.
	s.dropPlan(p)
	res.Applied = true
	if warn != nil {
		// loaded and in force; only a follow-up step failed
		s.alog.Warnf("config load: %v", warn)
		s.notify(Notice{Kind: "error", Title: i18n.T("配置已加载，但有一步未完成"), Detail: warn.Error()})
	}
	for _, id := range p.fresh {
		id := id
		s.goBackground(func() { s.downloadSynced(id) })
	}
	return res, nil
}

// summary says what loading the plan does, in one sentence.
func (p *applyPlan) summary() string {
	names := map[string]string{
		"settings": i18n.T("设置"), "overrides": i18n.T("覆写"), "customRules": i18n.T("自定义规则"),
		"manualNodes": i18n.T("手动节点"), "proxyChains": i18n.T("链式代理"), "subscriptions": i18n.T("订阅"),
	}
	var parts []string
	for _, c := range p.changed {
		parts = append(parts, names[c])
	}
	out := i18n.Tf("更新了%s", strings.Join(parts, i18n.T("、")))
	if n := len(p.missing); n > 0 {
		out += i18n.Tf("；%d 个订阅需要重新输入链接", n)
	}
	if n := len(p.missingNodes); n > 0 {
		out += i18n.Tf("；%d 个手动节点需要重新填写凭据", n)
	}
	if p.keptCur != "" {
		out += i18n.Tf("；正在使用的订阅「%s」已保留", p.keptCur)
	}
	return out
}

// checkPlan validates the plan before anything is stored: the ports,
// and the composed config of the current subscription with the core.
func (s *Service) checkPlan(p *applyPlan) error {
	if err := s.checkSettingsChange(p.cur.set, p.next.set); err != nil {
		return &docError{"settings", err}
	}
	cur, ok := s.d.store.Current()
	if !ok && len(readyManualNodes(p.next.manualNodes)) == 0 {
		return nil // nothing to compose yet
	}
	if ok {
		if _, err := s.d.store.Content(cur.ID); err != nil {
			return nil // not downloaded yet: the core checks it when it is
		}
	}
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	if bin == "" {
		return nil
	}
	sel := cur.Overrides
	for _, sub := range p.next.subs {
		if sub.ID == cur.ID {
			sel = sub.Overrides
		}
	}
	set := p.next.set
	ovs := p.next.ovs
	pr, err := s.prepare(composeArgs{
		settings:       &set,
		custom:         p.next.rules,
		useRules:       true,
		manualNodes:    p.next.manualNodes,
		useManualNodes: true,
		chains:         p.next.chains,
		useChains:      true,
		ovs:            &ovs,
		subOverrides:   &sel,
	})
	if err != nil {
		return err
	}
	pr.discard()
	return nil
}

// storePlan writes the subscriptions, overrides and rules of the plan.
// Subscriptions the file drops are deleted later (dropPlan).
func (s *Service) storePlan(p *applyPlan) error {
	st := s.d.store
	for _, sub := range p.put {
		if _, err := st.Put(sub); err != nil {
			return err
		}
	}
	if err := s.storeOverrides(p.next.ovs); err != nil {
		return err
	}
	if !manualNodesEqual(p.cur.manualNodes, p.next.manualNodes) {
		if _, err := st.SetManualNodes(p.next.manualNodes); err != nil {
			return err
		}
	}
	if !chainsEqual(p.cur.chains, p.next.chains) {
		if _, err := st.SetProxyChains(p.next.chains); err != nil {
			return err
		}
	}
	if !rulesEqual(p.cur.rules, p.next.rules) {
		if _, err := st.SetCustomRules(p.next.rules); err != nil {
			return err
		}
	}
	s.refreshStore(TopicSubs, TopicSettings, TopicRules, TopicProxies)
	return nil
}

// storeOverrides makes the store's override list exactly want: update or
// add, drop the others, then order as want.
func (s *Service) storeOverrides(want []model.Override) error {
	st := s.d.store
	keep := map[string]bool{}
	for _, o := range want {
		keep[o.ID] = true
		if old, ok := st.GetOverride(o.ID); ok && old == o {
			continue
		}
		if _, err := st.PutOverride(o); err != nil {
			return err
		}
	}
	for _, o := range st.Overrides() {
		if !keep[o.ID] {
			if err := st.DeleteOverride(o.ID); err != nil {
				return err
			}
		}
	}
	for i, o := range want {
		for j, have := range st.Overrides() {
			if have.ID == o.ID && j != i {
				if err := st.MoveOverride(o.ID, i-j); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// dropPlan deletes the URL subscriptions the file no longer has, with
// their keychain links, updates unresolved manual node keychain references,
// and forgets credentials of deleted or cleared manual nodes. It runs after
// everything else succeeded.
func (s *Service) dropPlan(p *applyPlan) {
	if p.replaceUnresolvedRefs {
		s.setUnresolvedNodeRefs(p.unresolvedRefs)
	}
	for _, id := range p.delNodes {
		s.forgetManualNodeSecrets(id)
	}
	for _, k := range p.clearedNodeSecrets {
		s.forgetManualNodeSecret(k.id, k.field)
	}
	if len(p.del) == 0 {
		return
	}
	for _, sub := range p.del {
		if err := s.d.store.Delete(sub.ID); err != nil {
			s.alog.Warnf("config load: delete subscription %s: %v", sub.ID, err)
			continue
		}
		s.forgetLinkSecret(sub.ID)
	}
	s.refreshStore(TopicSubs, TopicSettings)
}

// restorePlan puts the store back to what it was before storePlan: the
// override list, the custom rules, and every subscription record the
// plan (or dropping an override, which also leaves the selections of
// subscriptions) changed; subscriptions the plan added are removed. It
// goes on after an error and returns the first.
func (s *Service) restorePlan(p *applyPlan) error {
	st := s.d.store
	var first error
	note := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	note(s.storeOverrides(p.cur.ovs))
	if !rulesEqual(p.cur.rules, st.CustomRules()) {
		_, err := st.SetCustomRules(p.cur.rules)
		note(err)
	}
	if !manualNodesEqual(p.cur.manualNodes, st.ManualNodes()) {
		_, err := st.SetManualNodes(p.cur.manualNodes)
		note(err)
	}
	if !chainsEqual(p.cur.chains, st.ProxyChains()) {
		_, err := st.SetProxyChains(p.cur.chains)
		note(err)
	}
	was := map[string]bool{}
	for _, old := range p.cur.subs {
		was[old.ID] = true
		if now, ok := st.Get(old.ID); !ok || !subEqual(now, old) {
			_, err := st.Put(old)
			note(err)
		}
	}
	for _, sub := range p.put {
		if !was[sub.ID] {
			if _, ok := st.Get(sub.ID); ok {
				note(st.Delete(sub.ID))
			}
		}
	}
	s.refreshStore(TopicSubs, TopicSettings, TopicRules, TopicProxies)
	return first
}

func readyManualNodes(nodes []model.ManualNode) []model.ManualNode {
	var out []model.ManualNode
	for _, n := range nodes {
		if !profile.ManualNodeMissingSecret(n) {
			out = append(out, n)
		}
	}
	return out
}

func manualNodesEqual(a, b []model.ManualNode) bool {
	return slices.EqualFunc(a, b, func(x, y model.ManualNode) bool {
		return reflect.DeepEqual(model.CloneManualNode(x), model.CloneManualNode(y))
	})
}

func chainsEqual(a, b []model.ProxyChain) bool {
	return slices.EqualFunc(a, b, func(x, y model.ProxyChain) bool {
		return x.ID == y.ID && x.Name == y.Name && x.Enabled == y.Enabled && slices.Equal(x.Hops, y.Hops)
	})
}

func rulesEqual(a, b []model.CustomRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if ruleKey(a[i]) != ruleKey(b[i]) {
			return false
		}
	}
	return true
}

// applyPlanSettings brings the core up to date: SaveSettings for changed
// settings (hot, reload or restart as each change needs), a reload when
// only the store changed what the current subscription composes.
//
// err means the plan is not in force, and the settings are what they
// were: the caller puts the store back. warn means it is in force and
// only a follow-up step failed (the system proxy did not follow).
func (s *Service) applyPlanSettings(p *applyPlan) (warn, err error) {
	settingsChanged := false
	storeChanged := false
	for _, c := range p.changed {
		if c == "settings" {
			settingsChanged = true
		} else {
			storeChanged = true
		}
	}
	reloaded := false
	if settingsChanged {
		if err := s.saveSettingsFrom(p.next.set, true); err != nil {
			var w *settingsWarning
			if !errors.As(err, &w) {
				// refused or reverted (SaveSettings restores the previous
				// settings when applying them fails); make sure of it
				s.undoPlanSettings(p)
				return nil, &docError{"settings", err}
			}
			warn = err
		}
		reloaded = needsReload(p.cur.set, p.next.set) || advancedNeedsReload(p.cur.set.Advanced, p.next.set.Advanced)
	}
	if storeChanged && !reloaded && s.running() {
		s.opMu.Lock()
		var err error
		if _, hasSub := s.d.store.Current(); !hasSub && len(readyManualNodes(p.next.manualNodes)) == 0 {
			err = s.stopLocked()
		} else {
			err = s.reloadLocked()
		}
		s.opMu.Unlock()
		if err != nil {
			if settingsChanged {
				s.undoPlanSettings(p)
			}
			return nil, err
		}
	}
	return warn, nil
}

// undoPlanSettings makes the settings what they were before the plan,
// when a later step of loading it failed. Best effort: the error that
// made the load fail is the one reported.
func (s *Service) undoPlanSettings(p *applyPlan) {
	cur := s.Settings()
	back := syncfile.KeepLocal(copySettings(p.cur.set), cur) // machine-local items as they are now
	if reflect.DeepEqual(copySettings(cur), back) {
		return // SaveSettings refused the change, or already reverted it
	}
	if err := s.saveSettingsFrom(back, true); err != nil {
		s.alog.Warnf("config load: restore the previous settings: %v", err)
	}
}

// downloadSynced fetches a subscription that arrived through the file;
// the first one becomes the current subscription when there is none.
func (s *Service) downloadSynced(id string) {
	if err := s.UpdateSubscription(id); err != nil {
		s.alog.Warnf("config sync: download %s: %v", id, err)
		return
	}
	if _, ok := s.d.store.Current(); !ok {
		if err := s.UseSubscription(id); err != nil {
			s.alog.Warnf("config sync: use %s: %v", id, err)
		}
	}
}

// ApplyConfigFile implements API.ApplyConfigFile: loads a lanflow.yaml
// (path "" = the sync file) like an external change of the sync file —
// checked with the core first, nothing stored when anything fails. A dry
// run returns the diff only. The diff is this machine's lanflow.yaml
// text, links as keychain references, so it never reveals them.
func (s *Service) ApplyConfigFile(path string, dryRun bool) (res model.ConfigApplyResult, err error) {
	defer func() {
		if !dryRun {
			s.logOp("apply config file "+filepath.Base(path), err)
		}
	}()
	syncPath := ""
	if dir := s.syncDir(s.Settings().Sync); dir != "" {
		syncPath = filepath.Join(dir, syncfile.FileName)
	}
	if strings.TrimSpace(path) == "" {
		if syncPath == "" {
			return res, errors.New(i18n.T("配置同步未开启，请指定要加载的文件。"))
		}
		path = syncPath
	}
	if !filepath.IsAbs(path) {
		return res, errors.New(i18n.T("请使用文件的绝对路径。"))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return res, fmt.Errorf(i18n.T("读取文件失败：%v"), err)
	}
	if fi.Size() > 8<<20 {
		return res, errors.New(i18n.T("文件过大，不像是澜流的设置文件。"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return res, fmt.Errorf(i18n.T("读取文件失败：%v"), err)
	}
	doc, err := syncfile.Decode(data, defaultSettings())
	if err != nil {
		return res, err
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	plain, webdav := s.unsealDoc(doc)
	res, err = s.applyDoc(plain, dryRun)
	if err != nil {
		var de *docError
		if errors.As(err, &de) {
			if line := syncfile.LineOf(data, de.path); line > 0 {
				err = fmt.Errorf(i18n.T("第 %d 行：%w"), line, err)
			}
		}
		return res, err
	}
	if !dryRun && path == syncPath {
		// loaded: the poller must not load it again
		s.rememberHash(path, syncfile.Hash(data))
		s.releaseHeld()
		s.primeSeal(doc)
		s.adoptWebDAVPassword(webdav)
		s.cs.mu.Lock()
		s.cs.canon, s.cs.dirty = syncfile.Encode(doc), true
		s.cs.mu.Unlock()
	}
	return res, nil
}
