package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"lanflow/internal/model"
)

// selectionsFile keeps the node picked in every select group, per
// subscription: {"<subscription id>": {"<group>": "<member>"}}.
//
// The core's own cache.db (profile.store-selected) remembers selections
// too, but only by group name and only while that file survives: two
// subscriptions with the same group names overwrite each other, cleaning
// the core cache forgets them, and a core that cannot lock cache.db at
// start (the previous one still exiting) neither restores nor saves any.
// Lanflow's own record is applied after every start and reload, on top of
// whatever the core restored.
const selectionsFile = "selections.json"

func (s *Service) selectionsPath() string { return filepath.Join(s.opt.DataDir, selectionsFile) }

// loadSelectionsLocked reads the record once; selMu must be held.
func (s *Service) loadSelectionsLocked() map[string]map[string]string {
	if s.selections != nil {
		return s.selections
	}
	s.selections = map[string]map[string]string{}
	if b, err := os.ReadFile(s.selectionsPath()); err == nil {
		_ = json.Unmarshal(b, &s.selections)
	}
	return s.selections
}

// selectionKey is the current subscription's id ("" when there is none).
func (s *Service) selectionKey() string {
	if cur, ok := s.d.store.Current(); ok {
		return cur.ID
	}
	return ""
}

// rememberSelection records a selection the user made.
func (s *Service) rememberSelection(group, name string) {
	if group == probeGroup {
		return
	}
	key := s.selectionKey()
	s.selMu.Lock()
	defer s.selMu.Unlock()
	all := s.loadSelectionsLocked()
	if all[key] == nil {
		all[key] = map[string]string{}
	}
	if all[key][group] == name {
		return
	}
	if prev := all[key][group]; prev != "" {
		if s.selPrev == nil {
			s.selPrev = map[string]map[string]string{}
		}
		if s.selPrev[key] == nil {
			s.selPrev[key] = map[string]string{}
		}
		s.selPrev[key][group] = prev
	}
	all[key][group] = name
	// Drop the record of subscriptions that no longer exist.
	subs := s.d.store.List()
	for id := range all {
		if id != "" && id != key && !slices.ContainsFunc(subs, func(sub model.Subscription) bool { return sub.ID == id }) {
			delete(all, id)
		}
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	if err := writeFileAtomic(s.selectionsPath(), data, 0o600); err != nil {
		s.alog.Warnf("save selections: %v", err)
	}
}

// restoreSelections re-selects the remembered members of the current
// subscription's select groups where the core shows something else.
// Groups or members that are gone are skipped.
func (s *Service) restoreSelections() {
	key := s.selectionKey()
	s.selMu.Lock()
	want := make(map[string]string, len(s.loadSelectionsLocked()[key]))
	for g, n := range s.selections[key] {
		want[g] = n
	}
	s.selMu.Unlock()
	if len(want) == 0 {
		return
	}
	changed := 0
	for _, g := range s.Proxies().Groups {
		name, ok := want[g.Name]
		if !ok || g.Now == name || !slices.Contains(g.All, name) {
			continue
		}
		if g.Type != model.GroupSelect && g.Name != "GLOBAL" {
			continue
		}
		// Cancelled by Shutdown (this runs under opMu, which Shutdown waits
		// for), and given up at the first timeout: a controller that does
		// not answer one group will not answer the next ones either.
		ctx, cancel := s.svcCtx(ctrlTimeout)
		err := s.d.ctrl.SelectProxy(ctx, g.Name, name)
		gaveUp := ctx.Err() != nil
		cancel()
		if err != nil {
			s.alog.Warnf("restore selection %s -> %s: %v", g.Name, name, err)
			if gaveUp {
				break
			}
			continue
		}
		changed++
	}
	if changed > 0 {
		s.alog.Infof("restored %d group selection(s)", changed)
		s.refreshProxies()
	}
}

// renameSelections makes the record follow renamed members (a renamed
// chain, old name → new name), as members and as groups, so the next
// restoreSelections selects them again under their new names.
func (s *Service) renameSelections(renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	key := s.selectionKey()
	s.selMu.Lock()
	defer s.selMu.Unlock()
	all := s.loadSelectionsLocked()
	rec := all[key]
	if rec == nil {
		return
	}
	changed := false
	for g, n := range rec {
		if to, ok := renames[n]; ok {
			rec[g] = to
			changed = true
		}
	}
	for from, to := range renames {
		if n, ok := rec[from]; ok {
			delete(rec, from)
			rec[to] = n
			changed = true
		}
	}
	for g, n := range s.selPrev[key] {
		if to, ok := renames[n]; ok {
			s.selPrev[key][g] = to
		}
	}
	if !changed {
		return
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	if err := writeFileAtomic(s.selectionsPath(), data, 0o600); err != nil {
		s.alog.Warnf("save selections: %v", err)
	}
}

// previousSelection is what group had selected before its current
// member, as far as this run knows ("" = unknown).
func (s *Service) previousSelection(group string) string {
	key := s.selectionKey()
	s.selMu.Lock()
	defer s.selMu.Unlock()
	return s.selPrev[key][group]
}
