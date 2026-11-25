package app

import (
	"fmt"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// ManualNodes returns all user-defined proxy nodes in order.
func (s *Service) ManualNodes() []model.ManualNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.ManualNode, len(s.state.manualNodes))
	for i, n := range s.state.manualNodes {
		n.ALPN = append([]string(nil), n.ALPN...)
		out[i] = n
	}
	return out
}

// SaveManualNode validates, stores and applies a user-defined proxy node
// (adding when ID is empty, updating when ID matches). If the core is
// running it reloads; if no subscription exists and this is the first manual
// node, it starts the core when a binary is present.
func (s *Service) SaveManualNode(n model.ManualNode) (model.ManualNode, error) {
	n, err := profile.ValidateManualNode(n)
	if err != nil {
		return n, err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	old := s.d.store.ManualNodes()
	next := make([]model.ManualNode, 0, len(old)+1)
	next = append(next, old...)
	if n.ID == "" {
		next = append(next, n)
	} else {
		found := false
		for i := range next {
			if next[i].ID == n.ID {
				next[i] = n
				found = true
				break
			}
		}
		if !found {
			next = append(next, n)
		}
	}

	seen := map[string]bool{}
	for _, x := range next {
		if seen[x.Name] {
			return n, fmt.Errorf(i18n.T("已存在名为「%s」的手动节点，请换一个名称。"), x.Name)
		}
		seen[x.Name] = true
	}

	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()

	var p prepared
	if bin != "" {
		if p, err = s.prepare(composeArgs{manualNodes: next, useManualNodes: true}); err != nil {
			return n, err
		}
	}

	saved, err := s.d.store.SetManualNodes(next)
	if err != nil {
		p.discard()
		return n, fmt.Errorf(i18n.T("保存手动节点失败：%v"), err)
	}
	s.refreshStore(TopicProxies, TopicStatus)

	var outNode model.ManualNode
	for _, x := range saved {
		if (n.ID != "" && x.ID == n.ID) || (n.ID == "" && x.Name == n.Name) {
			outNode = x
			break
		}
	}

	_, hasSub := s.d.store.Current()
	if s.running() {
		if p.tmp != "" {
			if err := s.reloadPrepared(p); err != nil {
				_, _ = s.d.store.SetManualNodes(old)
				s.refreshStore(TopicProxies, TopicStatus)
				return n, fmt.Errorf(i18n.T("手动节点已撤销：%v"), err)
			}
		} else if err := s.reloadLocked(); err != nil {
			_, _ = s.d.store.SetManualNodes(old)
			s.refreshStore(TopicProxies, TopicStatus)
			return n, fmt.Errorf(i18n.T("手动节点已撤销：%v"), err)
		}
		return outNode, nil
	}

	p.discard()
	if !hasSub && len(old) == 0 && bin != "" {
		_ = s.startLocked()
	}
	return outNode, nil
}

// DeleteManualNode removes a user-defined proxy node by ID and updates the
// running core (stopping it if neither subscriptions nor manual nodes remain).
func (s *Service) DeleteManualNode(id string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	old := s.d.store.ManualNodes()
	next := make([]model.ManualNode, 0, len(old))
	for _, x := range old {
		if x.ID != id {
			next = append(next, x)
		}
	}
	if len(next) == len(old) {
		return nil
	}

	_, hasSub := s.d.store.Current()
	if !hasSub && len(next) == 0 {
		if _, err := s.d.store.SetManualNodes(next); err != nil {
			return fmt.Errorf(i18n.T("保存手动节点失败：%v"), err)
		}
		s.refreshStore(TopicProxies, TopicStatus)
		if s.running() {
			return s.stopLocked()
		}
		return nil
	}

	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()

	var p prepared
	if bin != "" && s.running() {
		var err error
		if p, err = s.prepare(composeArgs{manualNodes: next, useManualNodes: true}); err != nil {
			return err
		}
	}

	if _, err := s.d.store.SetManualNodes(next); err != nil {
		p.discard()
		return fmt.Errorf(i18n.T("保存手动节点失败：%v"), err)
	}
	s.refreshStore(TopicProxies, TopicStatus)

	if s.running() {
		if p.tmp != "" {
			return s.reloadPrepared(p)
		}
		return s.reloadLocked()
	}
	p.discard()
	return nil
}
