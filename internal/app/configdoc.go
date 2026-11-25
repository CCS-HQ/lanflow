package app

import (
	"errors"
	"fmt"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// lanflow.yaml (package syncfile) from and to the service's state. The
// same document serves configuration sync (configsync.go), where links
// are keychain references, and the export / WebDAV backup, where they
// are written out when the user asks for them.

// docLinks says how buildDoc writes subscription links.
type docLinks int

const (
	linksNone     docLinks = iota // no subscriptions at all (export without links)
	linksPlain                    // the links themselves (export with links, backup)
	linksKeychain                 // keychain:sub-<id> (sync)
)

// localState is the configuration a document is built from.
type localState struct {
	set         model.Settings
	ovs         []model.Override
	rules       []model.CustomRule
	manualNodes []model.ManualNode
	chains      []model.ProxyChain
	subs        []model.Subscription
}

// snapshotLocal reads the configuration (the store from disk; call
// without mu).
func (s *Service) snapshotLocal() localState {
	s.mu.Lock()
	set := copySettings(s.state.settings)
	s.mu.Unlock()
	return localState{
		set:         set,
		ovs:         s.d.store.Overrides(),
		rules:       s.d.store.CustomRules(),
		manualNodes: s.d.store.ManualNodes(),
		chains:      s.d.store.ProxyChains(),
		subs:        s.d.store.List(),
	}
}

// buildDoc turns st into lanflow.yaml's document.
func buildDoc(st localState, links docLinks) syncfile.Doc {
	doc := syncfile.Doc{Format: syncfile.Format, Settings: copySettings(st.set)}
	synced := map[string]bool{} // subscriptions in the document
	if links != linksNone {
		for _, sub := range st.subs {
			if sub.Source == urlSubscriptionK {
				synced[sub.ID] = true
			}
		}
	}
	local := map[string]bool{} // subscriptions on this machine only
	for _, sub := range st.subs {
		if !synced[sub.ID] {
			local[sub.ID] = true
		}
	}
	listed := map[string]bool{}
	for _, o := range st.ovs {
		// A subscription's legacy entry travels with its subscription.
		if local[o.ID] {
			continue
		}
		listed[o.ID] = true
		e := syncfile.Override{ID: o.ID, Name: o.Name, Enabled: o.Enabled, Global: o.Global, Text: o.Text}
		if o.Source == model.OverrideURL {
			e.URL = o.URL
		}
		doc.Overrides = append(doc.Overrides, e)
	}
	for _, r := range st.rules {
		doc.CustomRules = append(doc.CustomRules, syncfile.Rule{Type: r.Type, Payload: r.Payload, Target: r.Target})
	}
	if links != linksNone {
		doc.ManualNodes = []syncfile.ManualNode{} // always written when secrets are included: absent key keeps each machine's nodes
		for _, n := range st.manualNodes {
			e := manualNodeToDoc(n)
			if links == linksKeychain {
				for _, f := range syncfile.ManualNodeSecretFields {
					if p := e.SecretFieldPtr(f); p != nil && *p != "" {
						*p = syncfile.ManualNodeSecretRef(n.ID, f)
					}
				}
			}
			doc.ManualNodes = append(doc.ManualNodes, e)
		}
	}
	doc.ProxyChains = []syncfile.Chain{} // always written: a file without the key keeps each machine's chains
	for _, c := range st.chains {
		doc.ProxyChains = append(doc.ProxyChains, syncfile.Chain{ID: c.ID, Name: c.Name, Hops: append([]string(nil), c.Hops...), Enabled: c.Enabled})
	}
	for _, sub := range st.subs {
		if !synced[sub.ID] {
			continue
		}
		e := syncfile.Subscription{
			ID: sub.ID, Name: sub.Name, UserAgent: sub.UserAgent, ViaProxy: sub.ViaProxy,
			AutoUpdateHours: sub.AutoUpdateHours, TimeoutSec: sub.TimeoutSec,
		}
		for _, id := range sub.Overrides {
			if listed[id] {
				e.Overrides = append(e.Overrides, id)
			}
		}
		switch {
		case links == linksKeychain && sub.URL != "":
			e.URL = syncfile.SubscriptionRef(sub.ID)
		case links == linksPlain:
			e.URL = sub.URL
		}
		doc.Subscriptions = append(doc.Subscriptions, e)
	}
	return doc
}

// docToExport turns a lanflow.yaml read for import into the export the
// import path applies; resolve gives a keychain reference's value
// ("" when this machine has none). Subscription links and manual nodes
// whose secrets cannot be resolved are listed.
func docToExport(doc syncfile.Doc, resolve func(ref string) string) (model.SettingsExport, []string, []string) {
	set := doc.Settings
	ex := model.SettingsExport{App: exportApp, Format: exportFormat, Settings: &set, Overrides: &model.Overrides{PerSub: map[string]string{}}}
	for _, o := range doc.Overrides {
		ex.Overrides.List = append(ex.Overrides.List, overrideFromDoc(o))
	}
	for _, r := range doc.CustomRules {
		ex.CustomRules = append(ex.CustomRules, model.CustomRule{Type: r.Type, Payload: r.Payload, Target: r.Target})
	}
	var missingNodes []string
	for _, e := range doc.ManualNodes {
		n := manualNodeFromDoc(e)
		nodeMissing := false
		for _, f := range syncfile.ManualNodeSecretFields {
			p := modelSecretPtr(&n, f)
			if p == nil || *p == "" {
				continue
			}
			switch {
			case syncfile.IsSealed(*p):
				nodeMissing = true
				*p = ""
			case strings.HasPrefix(*p, syncfile.KeychainPrefix):
				v := resolve(*p)
				if v == "" {
					nodeMissing = true
					*p = ""
				} else {
					*p = v
				}
			}
		}
		if nodeMissing {
			missingNodes = append(missingNodes, e.Name)
			continue
		}
		ex.ManualNodes = append(ex.ManualNodes, n)
	}
	ex.ProxyChains = chainsFromDoc(doc.ProxyChains)
	var missingSubs []string
	for _, sub := range doc.Subscriptions {
		u := sub.URL
		if strings.HasPrefix(u, syncfile.KeychainPrefix) {
			u = resolve(u)
		}
		if u == "" || syncfile.IsSealed(u) {
			missingSubs = append(missingSubs, sub.Name)
			continue
		}
		ex.Subscriptions = append(ex.Subscriptions, model.ExportedSubscription{
			Name: sub.Name, URL: u, UserAgent: sub.UserAgent, ViaProxy: sub.ViaProxy, AutoUpdateHours: sub.AutoUpdateHours,
		})
	}
	return ex, missingSubs, missingNodes
}

func overrideFromDoc(o syncfile.Override) model.Override {
	out := model.Override{ID: o.ID, Name: o.Name, Enabled: o.Enabled, Global: o.Global, Source: model.OverrideLocal, Text: o.Text}
	if o.URL != "" {
		out.Source, out.URL = model.OverrideURL, o.URL
	}
	return out
}

// secretStore returns the keychain the service uses (nil = none).
func (s *Service) secretStore() SecretStore {
	if s.cs.secrets != nil {
		return s.cs.secrets
	}
	return Secrets
}

// resolveRef reads a keychain reference; "" when the keychain does not
// have it (or there is no keychain).
func (s *Service) resolveRef(ref string) string {
	key, ok := syncfile.KeychainKey(ref)
	st := s.secretStore()
	if !ok || st == nil {
		return ""
	}
	v, err := st.Get(key)
	if err != nil {
		if !errors.Is(err, ErrSecretNotFound) {
			s.alog.Warnf("config sync: keychain read %s: %v", key, err)
		}
		return ""
	}
	return string(v)
}

// encodeExport is the export file: lanflow.yaml with the links written
// out when includeSubs, without any subscription otherwise.
func (s *Service) encodeExport(includeSubs bool) []byte {
	links := linksNone
	if includeSubs {
		links = linksPlain
	}
	return syncfile.Encode(buildDoc(s.snapshotLocal(), links))
}

// parseImport reads an import file: lanflow.yaml, or the JSON export of
// earlier versions. fromDoc is true for lanflow.yaml.
func (s *Service) parseImport(data []byte) (ex model.SettingsExport, notes []string, err error) {
	if !syncfile.Sniff(data) {
		ex, err = parseExport(data)
		return ex, nil, err
	}
	doc, err := syncfile.Decode(data, defaultSettings())
	if err != nil {
		return ex, nil, fmt.Errorf(i18n.T("文件无效：%v"), err)
	}
	plain, _ := s.unsealDoc(doc)
	ex, missingSubs, missingNodes := docToExport(plain, s.resolveRef)
	for _, name := range missingSubs {
		notes = append(notes, fmt.Sprintf(i18n.T("订阅「%s」（文件中只有钥匙串引用，本机钥匙串里没有它的链接）"), name))
	}
	for _, name := range missingNodes {
		notes = append(notes, fmt.Sprintf(i18n.T("手动节点「%s」（文件中只有钥匙串引用，本机钥匙串里没有它的凭据）"), name))
	}
	for i, r := range ex.CustomRules {
		if _, err := validateCustomRule(r); err != nil {
			return ex, nil, fmt.Errorf(i18n.T("第 %d 条自定义规则无效：%v"), i+1, err)
		}
	}
	return ex, notes, nil
}

func manualNodeToDoc(n model.ManualNode) syncfile.ManualNode {
	var alpn []string
	if len(n.ALPN) > 0 {
		alpn = append([]string(nil), n.ALPN...)
	}
	return syncfile.ManualNode{
		ID:                       n.ID,
		Name:                     n.Name,
		Type:                     n.Type,
		Server:                   n.Server,
		Port:                     n.Port,
		UDP:                      n.UDP,
		Username:                 n.Username,
		Password:                 n.Password,
		UUID:                     n.UUID,
		Token:                    n.Token,
		AuthStr:                  n.AuthStr,
		Cipher:                   n.Cipher,
		AlterID:                  n.AlterID,
		Flow:                     n.Flow,
		PacketEncoding:           n.PacketEncoding,
		TLS:                      n.TLS,
		SNI:                      n.SNI,
		SkipCertVerify:           n.SkipCertVerify,
		ClientFingerprint:        n.ClientFingerprint,
		ALPN:                     alpn,
		RealityPublicKey:         n.RealityPublicKey,
		RealityShortID:           n.RealityShortID,
		Network:                  n.Network,
		WSPath:                   n.WSPath,
		WSHost:                   n.WSHost,
		GRPCServiceName:          n.GRPCServiceName,
		Plugin:                   n.Plugin,
		PluginMode:               n.PluginMode,
		PluginHost:               n.PluginHost,
		SSOptsEnabled:            n.SSOptsEnabled,
		SSOptsMethod:             n.SSOptsMethod,
		SSOptsPassword:           n.SSOptsPassword,
		Protocol:                 n.Protocol,
		Ports:                    n.Ports,
		Up:                       n.Up,
		Down:                     n.Down,
		Obfs:                     n.Obfs,
		ObfsPassword:             n.ObfsPassword,
		CongestionController:     n.CongestionController,
		UDPRelayMode:             n.UDPRelayMode,
		ReduceRTT:                n.ReduceRTT,
		DisableSNI:               n.DisableSNI,
		IdleSessionCheckInterval: n.IdleSessionCheckInterval,
		IdleSessionTimeout:       n.IdleSessionTimeout,
		MinIdleSession:           n.MinIdleSession,
	}
}

func manualNodeFromDoc(n syncfile.ManualNode) model.ManualNode {
	var alpn []string
	if len(n.ALPN) > 0 {
		alpn = append([]string(nil), n.ALPN...)
	}
	return model.ManualNode{
		ID:                       n.ID,
		Name:                     n.Name,
		Type:                     n.Type,
		Server:                   n.Server,
		Port:                     n.Port,
		UDP:                      n.UDP,
		Username:                 n.Username,
		Password:                 n.Password,
		UUID:                     n.UUID,
		Token:                    n.Token,
		AuthStr:                  n.AuthStr,
		Cipher:                   n.Cipher,
		AlterID:                  n.AlterID,
		Flow:                     n.Flow,
		PacketEncoding:           n.PacketEncoding,
		TLS:                      n.TLS,
		SNI:                      n.SNI,
		SkipCertVerify:           n.SkipCertVerify,
		ClientFingerprint:        n.ClientFingerprint,
		ALPN:                     alpn,
		RealityPublicKey:         n.RealityPublicKey,
		RealityShortID:           n.RealityShortID,
		Network:                  n.Network,
		WSPath:                   n.WSPath,
		WSHost:                   n.WSHost,
		GRPCServiceName:          n.GRPCServiceName,
		Plugin:                   n.Plugin,
		PluginMode:               n.PluginMode,
		PluginHost:               n.PluginHost,
		SSOptsEnabled:            n.SSOptsEnabled,
		SSOptsMethod:             n.SSOptsMethod,
		SSOptsPassword:           n.SSOptsPassword,
		Protocol:                 n.Protocol,
		Ports:                    n.Ports,
		Up:                       n.Up,
		Down:                     n.Down,
		Obfs:                     n.Obfs,
		ObfsPassword:             n.ObfsPassword,
		CongestionController:     n.CongestionController,
		UDPRelayMode:             n.UDPRelayMode,
		ReduceRTT:                n.ReduceRTT,
		DisableSNI:               n.DisableSNI,
		IdleSessionCheckInterval: n.IdleSessionCheckInterval,
		IdleSessionTimeout:       n.IdleSessionTimeout,
		MinIdleSession:           n.MinIdleSession,
	}
}

func modelSecretPtr(n *model.ManualNode, field string) *string {
	switch field {
	case "password":
		return &n.Password
	case "uuid":
		return &n.UUID
	case "token":
		return &n.Token
	case "authStr":
		return &n.AuthStr
	case "ssOptsPassword":
		return &n.SSOptsPassword
	case "obfsPassword":
		return &n.ObfsPassword
	default:
		return nil
	}
}

// chainsFromDoc converts the file's chains (nil stays nil: no key).
func chainsFromDoc(in []syncfile.Chain) []model.ProxyChain {
	if in == nil {
		return nil
	}
	out := make([]model.ProxyChain, 0, len(in))
	for _, c := range in {
		out = append(out, model.ProxyChain{ID: c.ID, Name: c.Name, Hops: append([]string(nil), c.Hops...), Enabled: c.Enabled})
	}
	return out
}
