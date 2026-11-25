// Package syncfile is the format of lanflow.yaml, the declarative file
// configuration sync keeps in a synced folder (docs/config-sync.md): its
// Go types, the YAML codec with line-numbered errors, the JSON Schema,
// and the file-system rules of synced folders (conflict copies, iCloud
// placeholders). It knows nothing of the running service; package app
// turns a Doc into settings, overrides, rules and subscriptions.
package syncfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

const (
	// Format is the version of the file this build writes and reads.
	// A file with a higher format is refused, never half-read.
	Format = 1
	// FileName is the file's name in the sync folder.
	FileName = "lanflow.yaml"
	// KeychainPrefix starts a reference to a secret in the system keychain.
	KeychainPrefix = "keychain:"
	// WebDAVPasswordRef is the reference written when a WebDAV password is set.
	WebDAVPasswordRef = KeychainPrefix + "webdav-password"
)

// Doc is lanflow.yaml.
type Doc struct {
	Format int `sync:"format"`
	// Encryption is set when the secrets are sealed with the sync
	// passphrase (seal.go): subscription links and the WebDAV password are
	// then enc:v1:<base64> values instead of keychain references.
	Encryption *Encryption `sync:"encryption,omitempty"`
	// Settings are model.Settings without the machine-local items of
	// Excluded.
	Settings      model.Settings `sync:"settings"`
	Overrides     []Override     `sync:"overrides"`
	CustomRules   []Rule         `sync:"customRules"`
	Subscriptions []Subscription `sync:"subscriptions"`
	// ManualNodes are the user's manually added outbound proxy nodes
	// (model.ManualNode). Credential fields (password, uuid, token,
	// authStr, ssOptsPassword, obfsPassword) are stored as keychain:<id>
	// references or enc:v1: sealed values. A file without the key leaves
	// this machine's manual nodes as they are; `manualNodes: []` removes them.
	ManualNodes []ManualNode `sync:"manualNodes"`
	// ProxyChains are the user's proxy chains (docs/chain-proxy.md): node
	// and group names only, no secrets. A file without the key (written
	// by a version without chains) leaves this machine's chains as they
	// are; `proxyChains: []` removes them.
	ProxyChains []Chain `sync:"proxyChains"`
	// WebDAVPassword is WebDAVPasswordRef when a password is set: the
	// password itself stays in the keychain. With Encryption it is the
	// sealed password (enc:v1:…).
	WebDAVPassword string `sync:"webdavPassword,omitempty"`
}

// Override is an entry of the override list (model.Override).
type Override struct {
	ID      string `sync:"id"`
	Name    string `sync:"name"`
	Enabled bool   `sync:"enabled"`
	Global  bool   `sync:"global"`
	// URL is set for an entry downloaded from a link (model.OverrideURL);
	// Text is then the last downloaded text.
	URL  string `sync:"url,omitempty"`
	Text string `sync:"text"`
}

// Rule is a custom rule (model.CustomRule); IDs are local.
type Rule struct {
	Type    string `sync:"type"`
	Payload string `sync:"payload"`
	Target  string `sync:"target"`
}

// Chain is a proxy chain (model.ProxyChain). The ID is the same on every
// machine: the generated nodes are named after it.
type Chain struct {
	ID      string   `sync:"id"`
	Name    string   `sync:"name"`
	Hops    []string `sync:"hops"`
	Enabled bool     `sync:"enabled"`
}

// ManualNode is a user-defined outbound proxy node (model.ManualNode).
// Credential fields hold "keychain:node-<id>-<field>" references, or
// enc:v1: sealed values when Encryption is set; plain strings are also
// accepted (when edited by a person or an agent) and moved into the
// keychain on the next write.
type ManualNode struct {
	ID     string `sync:"id"`
	Name   string `sync:"name"`
	Type   string `sync:"type"`
	Server string `sync:"server"`
	Port   int    `sync:"port"`
	UDP    bool   `sync:"udp,omitempty"`

	Username string `sync:"username,omitempty"`
	Password string `sync:"password,omitempty"`
	UUID     string `sync:"uuid,omitempty"`
	Token    string `sync:"token,omitempty"`
	AuthStr  string `sync:"authStr,omitempty"`

	Cipher  string `sync:"cipher,omitempty"`
	AlterID int    `sync:"alterId,omitempty"`

	Flow           string `sync:"flow,omitempty"`
	PacketEncoding string `sync:"packetEncoding,omitempty"`

	TLS               bool     `sync:"tls,omitempty"`
	SNI               string   `sync:"sni,omitempty"`
	SkipCertVerify    bool     `sync:"skipCertVerify,omitempty"`
	ClientFingerprint string   `sync:"clientFingerprint,omitempty"`
	ALPN              []string `sync:"alpn,omitempty"`
	RealityPublicKey  string   `sync:"realityPublicKey,omitempty"`
	RealityShortID    string   `sync:"realityShortId,omitempty"`

	Network         string `sync:"network,omitempty"`
	WSPath          string `sync:"wsPath,omitempty"`
	WSHost          string `sync:"wsHost,omitempty"`
	GRPCServiceName string `sync:"grpcServiceName,omitempty"`

	Plugin     string `sync:"plugin,omitempty"`
	PluginMode string `sync:"pluginMode,omitempty"`
	PluginHost string `sync:"pluginHost,omitempty"`

	SSOptsEnabled  bool   `sync:"ssOptsEnabled,omitempty"`
	SSOptsMethod   string `sync:"ssOptsMethod,omitempty"`
	SSOptsPassword string `sync:"ssOptsPassword,omitempty"`

	Protocol     string `sync:"protocol,omitempty"`
	Ports        string `sync:"ports,omitempty"`
	Up           string `sync:"up,omitempty"`
	Down         string `sync:"down,omitempty"`
	Obfs         string `sync:"obfs,omitempty"`
	ObfsPassword string `sync:"obfsPassword,omitempty"`

	CongestionController string `sync:"congestionController,omitempty"`
	UDPRelayMode         string `sync:"udpRelayMode,omitempty"`
	ReduceRTT            bool   `sync:"reduceRtt,omitempty"`
	DisableSNI           bool   `sync:"disableSni,omitempty"`

	IdleSessionCheckInterval int `sync:"idleSessionCheckInterval,omitempty"`
	IdleSessionTimeout       int `sync:"idleSessionTimeout,omitempty"`
	MinIdleSession           int `sync:"minIdleSession,omitempty"`
}

// ManualNodeSecretFields lists the YAML field names of ManualNode that
// carry credentials and are stored in the keychain or sealed with the
// sync passphrase.
var ManualNodeSecretFields = []string{"password", "uuid", "token", "authStr", "ssOptsPassword", "obfsPassword"}

// ManualNodeSecretKey is the keychain key of manual node id's field.
func ManualNodeSecretKey(id, field string) string { return "node-" + id + "-" + field }

// ManualNodeSecretRef is the keychain reference of manual node id's field.
func ManualNodeSecretRef(id, field string) string {
	return KeychainPrefix + ManualNodeSecretKey(id, field)
}

// SecretFieldPtr returns a pointer to the named credential field on n.
func (n *ManualNode) SecretFieldPtr(field string) *string {
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

// Subscription is a subscription with a link (model.Subscription with
// Source "url"); subscriptions imported from a local file stay local.
type Subscription struct {
	// ID is the same on every machine, so override selections and Wi-Fi
	// rules keep pointing at it.
	ID   string `sync:"id"`
	Name string `sync:"name"`
	// URL is "keychain:sub-<id>" as this app writes it, or the sealed link
	// (enc:v1:…) when Encryption is set; a plain http(s) link is accepted
	// too (an agent or person editing the file) and moved into the
	// keychain on the next write. "" = no link known.
	URL             string   `sync:"url"`
	UserAgent       string   `sync:"userAgent,omitempty"`
	ViaProxy        bool     `sync:"viaProxy,omitempty"`
	AutoUpdateHours int      `sync:"autoUpdateHours,omitempty"`
	TimeoutSec      int      `sync:"timeoutSec,omitempty"`
	Overrides       []string `sync:"overrides,omitempty"`
}

// SubscriptionRef is the keychain reference of subscription id's link.
func SubscriptionRef(id string) string { return KeychainPrefix + SubscriptionKey(id) }

// SubscriptionKey is the keychain key of subscription id's link.
func SubscriptionKey(id string) string { return "sub-" + id }

// KeychainKey returns the key of a "keychain:<key>" reference.
func KeychainKey(ref string) (string, bool) {
	k, ok := strings.CutPrefix(ref, KeychainPrefix)
	return k, ok && k != ""
}

// ---- machine-local items ----

// ExcludedItem is a part of the configuration that stays on each machine.
type ExcludedItem struct {
	Path   string // YAML path in the file, as it would be named
	Reason string // for docs and tests (English)
}

// Excluded lists what the file leaves out. Runtime data (caches,
// statistics, runtime.yaml, window positions) is not configuration and
// never was in it; this list is the configuration that stays local.
var Excluded = []ExcludedItem{
	{"settings.ui", "interface state"},
	{"settings.connHiddenColumns", "interface state"},
	{"settings.openAtLogin", "a login item of this machine"},
	{"settings.tun", "TUN needs the helper installed on this machine"},
	{"settings.corePath", "a path on this machine"},
	{"settings.coreVersion", "a core version installed on this machine"},
	{"settings.geo.updatedAt", "a record of what ran"},
	{"settings.webDAV.lastBackupAt", "a record of what ran"},
	{"settings.agentGrants", "this machine's answers to agents"},
	{"settings.feedback.autoReport", "consent to send error reports, given on this machine"},
	{"settings.sync", "the sync setting itself"},
	{"settings.advanced.tunDevice", "an interface name of this machine"},
	{"settings.advanced.tunIncludeInterface", "an interface name of this machine"},
	{"settings.advanced.tunExcludeInterface", "an interface name of this machine"},
	{"settings.advanced.interfaceName", "an interface name of this machine"},
}

func isExcluded(path string) bool {
	for _, e := range Excluded {
		if e.Path == path {
			return true
		}
	}
	return false
}

// KeepLocal returns file with the machine-local items of Excluded taken
// from local: the settings this machine ends up with when it loads file.
func KeepLocal(file, local model.Settings) model.Settings {
	dst := reflect.ValueOf(&file).Elem()
	copyExcluded(dst, reflect.ValueOf(local), "settings")
	return file
}

func copyExcluded(dst, src reflect.Value, path string) {
	t := dst.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, skip := keyOf(t.Field(i))
		if skip {
			continue
		}
		p := join(path, name)
		switch {
		case isExcluded(p):
			dst.Field(i).Set(src.Field(i))
		case t.Field(i).Type.Kind() == reflect.Struct && t.Field(i).Type != timeType:
			copyExcluded(dst.Field(i), src.Field(i), p)
		}
	}
}

// ---- encode / decode ----

// header starts every file this app writes (comments are not kept on
// reading: the file is rewritten from the configuration).
const header = `# Lanflow configuration (docs/config-sync.md), schema: docs/schema/lanflow.schema.json
# Lanflow validates and loads changes to this file; an invalid change is refused
# and the settings show where. Secrets are keychain:<id> references, or
# enc:v1: values sealed with the sync passphrase.
`

// Encode writes doc as lanflow.yaml. The output is canonical: the same
// Doc always gives the same bytes, so comparing encodings compares
// configurations.
func Encode(doc Doc) []byte {
	doc.Format = Format
	node := encodeValue(reflect.ValueOf(doc), "")
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		panic("syncfile: encode: " + err.Error()) // nodes built here are always encodable
	}
	_ = enc.Close()
	return buf.Bytes()
}

// Decode reads lanflow.yaml. Settings missing from the file keep the
// values of defaults; machine-local items (see Excluded) are
// ignored and left at their defaults, for KeepLocal to fill. Errors are *Error with
// the line when it is known.
func Decode(data []byte, defaults model.Settings) (Doc, error) {
	doc, _, err := DecodeWarn(data, defaults)
	return doc, err
}

// DecodeWarn is Decode that also returns the warnings: keys this version
// does not know, which it ignores.
func DecodeWarn(data []byte, defaults model.Settings) (Doc, []*Error, error) {
	doc, d, err := decode(data, defaults)
	return doc, d.warnings, err
}

func decode(data []byte, defaults model.Settings) (Doc, *decoder, error) {
	d := &decoder{}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Doc{}, d, yamlErr(err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return Doc{}, d, &Error{Msg: i18n.T("文件是空的")}
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return Doc{}, d, errAt(top, "", i18n.T("文件顶层应为键值对象"))
	}
	f := mapValue(top, "format")
	if f == nil {
		return Doc{}, d, errAt(top, "format", i18n.T("缺少 format（文件格式版本）"))
	}
	ver, err := strconv.Atoi(f.Value)
	if err != nil || f.Kind != yaml.ScalarNode || ver < 1 {
		return Doc{}, d, errAt(f, "format", i18n.T("format 应为正整数"))
	}
	if ver > Format {
		return Doc{}, d, errAt(f, "format", i18n.Tf("文件由更新版本的澜流写入（格式 %d），当前版本只能读取格式 %d", ver, Format))
	}
	doc := Doc{Settings: defaults}
	if err := d.decodeValue(top, reflect.ValueOf(&doc).Elem(), ""); err != nil {
		return Doc{}, d, err
	}
	if err := validate(doc, top); err != nil {
		return Doc{}, d, err
	}
	return doc, d, nil
}

// Foreign reports whether data is a lanflow.yaml this build must never
// rewrite, because writing it back would destroy what another device put
// there: a file in a newer format (Decode refuses it), or one whose
// secrets are sealed in a way this build does not know (another key
// derivation, or sealed values of a version other than enc:v1:). An older
// build that rewrote such a file from its own configuration would turn
// encryption off for every device, or drop what the newer format added.
// Anything else that does not load is merely invalid, not foreign.
func Foreign(data []byte) bool {
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil || len(root.Content) == 0 {
		return false
	}
	top := root.Content[0]
	if f := mapValue(top, "format"); f != nil && f.Kind == yaml.ScalarNode {
		if ver, err := strconv.Atoi(f.Value); err == nil && ver > Format {
			return true
		}
	}
	// a sealed value of another version: "enc:" without this build's prefix
	other := func(n *yaml.Node) bool {
		return n != nil && n.Kind == yaml.ScalarNode && strings.HasPrefix(n.Value, sealedScheme) && !IsSealed(n.Value)
	}
	if e := mapValue(top, "encryption"); e != nil {
		if e.Kind != yaml.MappingNode {
			return true
		}
		if k := mapValue(e, "kdf"); k == nil || k.Value != KDFArgon2id {
			return true
		}
		if other(mapValue(e, "check")) {
			return true
		}
	}
	if other(mapValue(top, "webdavPassword")) {
		return true
	}
	if subs := mapValue(top, "subscriptions"); subs != nil && subs.Kind == yaml.SequenceNode {
		for _, sub := range subs.Content {
			if other(mapValue(sub, "url")) {
				return true
			}
		}
	}
	if nodes := mapValue(top, "manualNodes"); nodes != nil && nodes.Kind == yaml.SequenceNode {
		for _, node := range nodes.Content {
			for _, f := range ManualNodeSecretFields {
				if other(mapValue(node, f)) {
					return true
				}
			}
		}
	}
	return false
}

// yamlErr turns a YAML syntax error into an *Error with its line.
func yamlErr(err error) error {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: ")
	e := &Error{Msg: i18n.T("YAML 语法错误：") + msg}
	if rest, ok := strings.CutPrefix(msg, "line "); ok {
		if n, tail, ok := strings.Cut(rest, ":"); ok {
			if line, err := strconv.Atoi(n); err == nil {
				e.Line, e.Msg = line, i18n.T("YAML 语法错误：")+strings.TrimSpace(tail)
			}
		}
	}
	var te *yaml.TypeError
	if errors.As(err, &te) && len(te.Errors) > 0 {
		e.Msg = i18n.T("YAML 语法错误：") + te.Errors[0]
	}
	return e
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var manualNodeTypes = map[string]bool{
	model.ProxyTypeSOCKS5:    true,
	model.ProxyTypeHTTP:      true,
	model.ProxyTypeSS:        true,
	model.ProxyTypeVMess:     true,
	model.ProxyTypeVLESS:     true,
	model.ProxyTypeTrojan:    true,
	model.ProxyTypeTrojanGo:  true,
	model.ProxyTypeHysteria:  true,
	model.ProxyTypeHysteria2: true,
	model.ProxyTypeTUIC:      true,
	model.ProxyTypeAnyTLS:    true,
}

// validate checks what the types cannot: IDs, references and links.
func validate(doc Doc, top *yaml.Node) error {
	subs := map[string]bool{}
	for i, s := range doc.Subscriptions {
		p := "subscriptions[" + strconv.Itoa(i) + "]"
		if !idPattern.MatchString(s.ID) {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("订阅 ID 只能由字母、数字、- 和 _ 组成（最多 64 个字符）"))
		}
		if subs[s.ID] {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("订阅 ID 重复"))
		}
		subs[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			return errAt(NodeAt(top, p), p+".name", i18n.T("订阅名称不能为空"))
		}
		if s.URL != "" && !IsSealed(s.URL) && !strings.HasPrefix(s.URL, KeychainPrefix) && !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
			return errAt(NodeAt(top, p+".url"), p+".url", i18n.T("订阅链接应为 keychain:<id> 引用、enc:v1: 加密值或 http(s) 链接"))
		}
		if err := validateSealed(doc, top, p+".url", s.URL); err != nil {
			return err
		}
		if _, ok := KeychainKey(s.URL); strings.HasPrefix(s.URL, KeychainPrefix) && !ok {
			return errAt(NodeAt(top, p+".url"), p+".url", i18n.T("keychain: 后面缺少名称"))
		}
		if s.AutoUpdateHours < 0 || s.TimeoutSec < 0 {
			return errAt(NodeAt(top, p), p, i18n.T("自动更新间隔和超时不能为负数"))
		}
	}
	if doc.Encryption != nil {
		if err := doc.Encryption.Validate(); err != nil {
			return errAt(NodeAt(top, "encryption"), "encryption", err.Error())
		}
	}
	if w := doc.WebDAVPassword; w != "" {
		if !IsSealed(w) && !strings.HasPrefix(w, KeychainPrefix) {
			return errAt(NodeAt(top, "webdavPassword"), "webdavPassword", i18n.T("webdavPassword 应为 keychain:<id> 引用或 enc:v1: 加密值"))
		}
		if err := validateSealed(doc, top, "webdavPassword", w); err != nil {
			return err
		}
	}
	ovs := map[string]bool{}
	for i, o := range doc.Overrides {
		p := "overrides[" + strconv.Itoa(i) + "]"
		if !idPattern.MatchString(o.ID) {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("覆写 ID 只能由字母、数字、- 和 _ 组成（最多 64 个字符）"))
		}
		if ovs[o.ID] {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("覆写 ID 重复"))
		}
		ovs[o.ID] = true
		if o.URL != "" && !strings.HasPrefix(o.URL, "http://") && !strings.HasPrefix(o.URL, "https://") {
			return errAt(NodeAt(top, p+".url"), p+".url", i18n.T("覆写链接应以 http:// 或 https:// 开头"))
		}
		var probe yaml.Node
		if err := yaml.Unmarshal([]byte(o.Text), &probe); err != nil {
			e := errAt(NodeAt(top, p+".text"), p+".text", i18n.T("覆写内容不是有效的 YAML：")+strings.TrimPrefix(err.Error(), "yaml: "))
			return e
		}
	}
	for i, s := range doc.Subscriptions {
		for j, id := range s.Overrides {
			if !ovs[id] {
				p := "subscriptions[" + strconv.Itoa(i) + "].overrides[" + strconv.Itoa(j) + "]"
				return errAt(NodeAt(top, p), p, i18n.Tf("覆写「%s」不在 overrides 列表中", id))
			}
		}
	}
	for i, r := range doc.CustomRules {
		p := "customRules[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(r.Type) == "" || strings.TrimSpace(r.Payload) == "" || strings.TrimSpace(r.Target) == "" {
			return errAt(NodeAt(top, p), p, i18n.T("自定义规则需要 type、payload 和 target"))
		}
	}
	nodeIDs, nodeNames := map[string]bool{}, map[string]bool{}
	for i := range doc.ManualNodes {
		n := &doc.ManualNodes[i]
		p := "manualNodes[" + strconv.Itoa(i) + "]"
		if !idPattern.MatchString(n.ID) {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("手动节点 ID 只能由字母、数字、- 和 _ 组成（最多 64 个字符）"))
		}
		if nodeIDs[n.ID] {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("手动节点 ID 重复"))
		}
		nodeIDs[n.ID] = true
		name := strings.TrimSpace(n.Name)
		if name == "" {
			return errAt(NodeAt(top, p), p+".name", i18n.T("手动节点名称不能为空"))
		}
		if nodeNames[name] {
			return errAt(NodeAt(top, p+".name"), p+".name", i18n.T("手动节点名称重复"))
		}
		nodeNames[name] = true
		typ := strings.ToLower(strings.TrimSpace(n.Type))
		if !manualNodeTypes[typ] {
			return errAt(NodeAt(top, p+".type"), p+".type", i18n.Tf("不支持的代理协议「%s」", n.Type))
		}
		if strings.TrimSpace(n.Server) == "" {
			return errAt(NodeAt(top, p), p+".server", i18n.T("服务器地址不能为空"))
		}
		if n.Port < 1 || n.Port > 65535 {
			return errAt(NodeAt(top, p+".port"), p+".port", i18n.T("端口须为 1–65535 之间的数字"))
		}
		if n.AlterID < 0 || n.IdleSessionCheckInterval < 0 || n.IdleSessionTimeout < 0 || n.MinIdleSession < 0 {
			return errAt(NodeAt(top, p), p, i18n.T("手动节点的数值选项不能为负数"))
		}
		for _, f := range ManualNodeSecretFields {
			v := *n.SecretFieldPtr(f)
			if v == "" {
				continue
			}
			fp := p + "." + f
			if strings.HasPrefix(v, sealedScheme) && !IsSealed(v) {
				return errAt(NodeAt(top, fp), fp, i18n.T("不支持的加密值版本，请更新澜流"))
			}
			if err := validateSealed(doc, top, fp, v); err != nil {
				return err
			}
			if strings.HasPrefix(v, KeychainPrefix) {
				if _, ok := KeychainKey(v); !ok {
					return errAt(NodeAt(top, fp), fp, i18n.T("keychain: 后面缺少名称"))
				}
			}
		}
	}
	chainIDs, chainNames := map[string]bool{}, map[string]bool{}
	for i, c := range doc.ProxyChains {
		p := "proxyChains[" + strconv.Itoa(i) + "]"
		if !idPattern.MatchString(c.ID) {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("链路 ID 只能由字母、数字、- 和 _ 组成（最多 64 个字符）"))
		}
		if chainIDs[c.ID] {
			return errAt(NodeAt(top, p+".id"), p+".id", i18n.T("链路 ID 重复"))
		}
		chainIDs[c.ID] = true
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return errAt(NodeAt(top, p), p+".name", i18n.T("链路名称不能为空"))
		}
		if chainNames[name] {
			return errAt(NodeAt(top, p+".name"), p+".name", i18n.T("链路名称重复"))
		}
		chainNames[name] = true
		if n := len(c.Hops); n < model.ProxyChainMinHops || n > model.ProxyChainMaxHops {
			return errAt(NodeAt(top, p+".hops"), p+".hops", i18n.Tf("链路需要 %d–%d 跳", model.ProxyChainMinHops, model.ProxyChainMaxHops))
		}
		for j, h := range c.Hops {
			if strings.TrimSpace(h) == "" {
				hp := p + ".hops[" + strconv.Itoa(j) + "]"
				return errAt(NodeAt(top, hp), hp, i18n.T("链路的跳不能为空"))
			}
		}
	}
	return nil
}

// validateSealed checks a sealed value's form, and that the file has the
// encryption block that says how to open it.
func validateSealed(doc Doc, top *yaml.Node, path, v string) error {
	if !IsSealed(v) {
		return nil
	}
	if doc.Encryption == nil {
		return errAt(NodeAt(top, path), path, i18n.T("有加密的值，但文件缺少 encryption（口令参数）"))
	}
	if err := checkSealedFormat(v); err != nil {
		return errAt(NodeAt(top, path), path, err.Error())
	}
	return nil
}

// NodeAt returns the node at path ("customRules[2].target") under the
// document's top mapping, nil when there is none.
func NodeAt(top *yaml.Node, path string) *yaml.Node {
	n := top
	for _, part := range strings.Split(path, ".") {
		if n == nil {
			return nil
		}
		key, rest, _ := strings.Cut(part, "[")
		if key != "" {
			n = mapValue(n, key)
		}
		for rest != "" && n != nil {
			idx, tail, _ := strings.Cut(rest, "]")
			i, err := strconv.Atoi(idx)
			if err != nil || n.Kind != yaml.SequenceNode || i < 0 || i >= len(n.Content) {
				return nil
			}
			n = n.Content[i]
			rest = strings.TrimPrefix(tail, "[")
		}
	}
	return n
}

// LineOf returns the line of path in data, 0 when it cannot be found.
func LineOf(data []byte, path string) int {
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil || len(root.Content) == 0 {
		return 0
	}
	if n := NodeAt(root.Content[0], path); n != nil {
		return n.Line
	}
	return 0
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// Sniff reports whether data looks like lanflow.yaml rather than the JSON
// export of older versions: a YAML mapping with a format key that is not
// JSON.
func Sniff(data []byte) bool {
	t := bytes.TrimSpace(data)
	if len(t) == 0 || t[0] == '{' {
		return false
	}
	var probe struct {
		Format int `yaml:"format"`
	}
	return yaml.Unmarshal(data, &probe) == nil && probe.Format > 0
}

// Hash is the content hash used to recognise a file already seen.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
