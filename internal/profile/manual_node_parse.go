package profile

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

var (
	// ErrClipboardEmpty reports that the clipboard holds no non-whitespace text.
	ErrClipboardEmpty = errors.New("clipboard is empty")
	// ErrUnsupportedNodeFormat reports that the clipboard text is not a recognized proxy node link or YAML.
	ErrUnsupportedNodeFormat = errors.New("unsupported node format")
)

var supportedURISchemes = []string{
	"ss://",
	"vmess://",
	"vless://",
	"trojan://",
	"trojan-go://",
	"hysteria2://",
	"hy2://",
	"hysteria://",
	"hy://",
	"tuic://",
	"anytls://",
	"socks5://",
	"socks://",
	"socks5+tls://",
	"http://",
	"https://",
}

// ParseManualNodeClipboard parses a proxy node from clipboard text, accepting
// proxy URIs, Clash/Mihomo YAML node snippets, or base64-encoded URIs.
func ParseManualNodeClipboard(raw string) (model.ManualNode, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return model.ManualNode{}, ErrClipboardEmpty
	}

	if line, ok := firstSupportedURILine(trimmed); ok {
		return parseNodeURI(line)
	}

	if node, handled, err := parseYAMLNodeSnippet(trimmed); handled {
		return node, err
	}

	if !strings.ContainsAny(trimmed, " \t") {
		compact := strings.ReplaceAll(strings.ReplaceAll(trimmed, "\r", ""), "\n", "")
		if dec, err := decodeBase64Flex(compact); err == nil {
			if line, ok := firstSupportedURILine(string(dec)); ok {
				return parseNodeURI(line)
			}
		}
	}

	return model.ManualNode{}, ErrUnsupportedNodeFormat
}

func firstSupportedURILine(text string) (string, bool) {
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		for _, prefix := range supportedURISchemes {
			if strings.HasPrefix(lower, prefix) {
				return line, true
			}
		}
	}
	return "", false
}

func parseNodeURI(raw string) (model.ManualNode, error) {
	idx := strings.Index(raw, "://")
	if idx < 0 {
		return model.ManualNode{}, ErrUnsupportedNodeFormat
	}
	scheme := strings.ToLower(raw[:idx])
	rest := raw[idx+3:]

	switch scheme {
	case "ss":
		return parseSSURI(rest)
	case "vmess":
		return parseVMessURI(rest)
	case "vless":
		return parseVLESSURI(rest)
	case "trojan":
		return parseTrojanURI(rest, false)
	case "trojan-go":
		return parseTrojanURI(rest, true)
	case "hysteria2", "hy2":
		return parseHysteria2URI(rest)
	case "hysteria", "hy":
		return parseHysteriaURI(rest)
	case "tuic":
		return parseTUICURI(rest)
	case "anytls":
		return parseAnyTLSURI(rest)
	case "socks5", "socks", "socks5+tls":
		return parseSOCKSURI(rest, scheme == "socks5+tls")
	case "http", "https":
		return parseHTTPURI(rest, scheme == "https")
	default:
		return model.ManualNode{}, ErrUnsupportedNodeFormat
	}
}

func splitFragment(s string) (mainPart, name string) {
	if idx := strings.IndexByte(s, '#'); idx >= 0 {
		frag := s[idx+1:]
		if unesc, err := url.PathUnescape(frag); err == nil {
			frag = unesc
		}
		return s[:idx], strings.TrimSpace(frag)
	}
	return s, ""
}

func splitQuery(s string) (before string, q url.Values, err error) {
	if idx := strings.IndexByte(s, '?'); idx >= 0 {
		q, err = url.ParseQuery(s[idx+1:])
		if err != nil {
			return "", nil, fmt.Errorf("invalid URI query: %w", err)
		}
		return s[:idx], q, nil
	}
	return s, url.Values{}, nil
}

func splitUserHostPort(s string, defaultPort int) (userinfo, host string, port int, hasUser bool, err error) {
	s = strings.TrimSpace(s)
	if idx := strings.LastIndexByte(s, '@'); idx >= 0 {
		userinfo = s[:idx]
		s = s[idx+1:]
		hasUser = true
	}
	if slash := strings.IndexByte(s, '/'); slash >= 0 {
		s = s[:slash]
	}
	host, port, err = splitHostPort(s, defaultPort)
	return userinfo, host, port, hasUser, err
}

func splitHostPort(hp string, defaultPort int) (string, int, error) {
	hp = strings.TrimSpace(hp)
	if hp == "" {
		return "", 0, errors.New("missing server host")
	}
	if strings.HasPrefix(hp, "[") {
		if end := strings.IndexByte(hp, ']'); end >= 0 && end+1 == len(hp) {
			if defaultPort <= 0 {
				return "", 0, errors.New("missing server port")
			}
			host := strings.TrimSpace(hp[1:end])
			if host == "" {
				return "", 0, errors.New("missing server host")
			}
			return host, defaultPort, nil
		}
		host, portStr, err := net.SplitHostPort(hp)
		if err != nil {
			return "", 0, fmt.Errorf("invalid host/port: %w", err)
		}
		port, err := parsePort(portStr)
		if err != nil {
			return "", 0, err
		}
		return host, port, nil
	}
	if strings.Count(hp, ":") == 1 {
		host, portStr, err := net.SplitHostPort(hp)
		if err != nil {
			return "", 0, fmt.Errorf("invalid host/port: %w", err)
		}
		if strings.TrimSpace(host) == "" {
			return "", 0, errors.New("missing server host")
		}
		port, err := parsePort(portStr)
		if err != nil {
			return "", 0, err
		}
		return strings.TrimSpace(host), port, nil
	}
	if strings.Contains(hp, ":") {
		return "", 0, errors.New("invalid host/port")
	}
	if defaultPort <= 0 {
		return "", 0, errors.New("missing server port")
	}
	return hp, defaultPort, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, errors.New("port must be between 1 and 65535")
	}
	return p, nil
}

func decodeBase64Flex(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty base64")
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64 encoding")
}

func pathUnescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func queryFirst(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func queryBool(q url.Values, keys ...string) bool {
	for _, k := range keys {
		switch strings.ToLower(strings.TrimSpace(q.Get(k))) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

func splitALPN(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func normalizeNetwork(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "tcp", "raw":
		return "tcp"
	case "ws", "websocket":
		return "ws"
	case "grpc":
		return "grpc"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func applyURINameFallback(n *model.ManualNode) {
	if strings.TrimSpace(n.Name) == "" {
		n.Name = n.Server
	}
}

func parseSSURI(rest string) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}

	var cipher, password, host string
	var port int

	if strings.Contains(beforeQuery, "@") {
		userinfo, h, p, _, err := splitUserHostPort(beforeQuery, 0)
		if err != nil {
			return model.ManualNode{}, err
		}
		host, port = h, p
		if !strings.Contains(userinfo, ":") {
			dec, err := decodeBase64Flex(userinfo)
			if err != nil {
				return model.ManualNode{}, errors.New("invalid Shadowsocks userinfo")
			}
			userinfo = string(dec)
		}
		sep := strings.IndexByte(userinfo, ':')
		if sep < 0 {
			return model.ManualNode{}, errors.New("invalid Shadowsocks credentials")
		}
		cipher = pathUnescape(userinfo[:sep])
		password = pathUnescape(userinfo[sep+1:])
	} else {
		trimmed := strings.TrimSuffix(strings.TrimSpace(beforeQuery), "/")
		dec, err := decodeBase64Flex(trimmed)
		if err != nil {
			return model.ManualNode{}, errors.New("invalid Shadowsocks URI")
		}
		userinfo, h, p, hasUser, err := splitUserHostPort(string(dec), 0)
		if err != nil || !hasUser {
			return model.ManualNode{}, errors.New("invalid Shadowsocks URI")
		}
		host, port = h, p
		sep := strings.IndexByte(userinfo, ':')
		if sep < 0 {
			return model.ManualNode{}, errors.New("invalid Shadowsocks credentials")
		}
		cipher = userinfo[:sep]
		password = userinfo[sep+1:]
	}

	n := model.ManualNode{
		Name:     name,
		Type:     model.ProxyTypeSS,
		Server:   host,
		Port:     port,
		Cipher:   cipher,
		Password: password,
		UDP:      true,
	}
	if pluginStr := queryFirst(q, "plugin"); pluginStr != "" {
		parts := strings.Split(pluginStr, ";")
		pname := strings.TrimSpace(parts[0])
		switch pname {
		case "obfs-local", "simple-obfs":
			n.Plugin = "obfs"
		default:
			n.Plugin = pname
		}
		for _, kv := range parts[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "obfs", "mode":
				n.PluginMode = strings.TrimSpace(v)
			case "obfs-host", "host":
				n.PluginHost = strings.TrimSpace(v)
			}
		}
	}
	applyURINameFallback(&n)
	return n, nil
}

type vmessJSON struct {
	PS   string          `json:"ps"`
	Add  string          `json:"add"`
	Port json.RawMessage `json:"port"`
	ID   string          `json:"id"`
	Aid  json.RawMessage `json:"aid"`
	Scy  string          `json:"scy"`
	Net  string          `json:"net"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  json.RawMessage `json:"tls"`
	SNI  string          `json:"sni"`
	FP   string          `json:"fp"`
	ALPN string          `json:"alpn"`
}

func jsonIntOrString(raw json.RawMessage) (int, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, nil
	}
	var i int
	if err := json.Unmarshal(raw, &i); err == nil {
		return i, nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		str = strings.TrimSpace(str)
		if str == "" {
			return 0, nil
		}
		return strconv.Atoi(str)
	}
	return 0, errors.New("expected integer or numeric string")
}

func jsonTLSBool(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		switch strings.ToLower(strings.TrimSpace(str)) {
		case "tls", "1", "true", "yes":
			return true
		}
	}
	return false
}

func parseVMessURI(rest string) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	if strings.Contains(beforeQuery, "@") {
		uuid, host, port, _, err := splitUserHostPort(beforeQuery, 0)
		if err != nil {
			return model.ManualNode{}, err
		}
		cipher := queryFirst(q, "encryption")
		if cipher == "" {
			cipher = "auto"
		}
		sec := strings.ToLower(queryFirst(q, "security"))
		netw := normalizeNetwork(queryFirst(q, "type"))
		n := model.ManualNode{
			Name:              name,
			Type:              model.ProxyTypeVMess,
			Server:            host,
			Port:              port,
			UUID:              pathUnescape(uuid),
			Cipher:            cipher,
			UDP:               true,
			TLS:               sec == "tls" || queryBool(q, "tls"),
			SNI:               queryFirst(q, "sni", "peer"),
			SkipCertVerify:    queryBool(q, "allowInsecure", "insecure", "skip-cert-verify"),
			ClientFingerprint: queryFirst(q, "fp"),
			ALPN:              splitALPN(queryFirst(q, "alpn")),
			Network:           netw,
		}
		switch netw {
		case "ws":
			n.WSPath = queryFirst(q, "path")
			n.WSHost = queryFirst(q, "host")
		case "grpc":
			n.GRPCServiceName = queryFirst(q, "serviceName", "path")
		}
		applyURINameFallback(&n)
		return n, nil
	}

	dec, err := decodeBase64Flex(strings.TrimSpace(beforeFrag))
	if err != nil {
		return model.ManualNode{}, errors.New("invalid VMess URI")
	}
	var v vmessJSON
	if err := json.Unmarshal(dec, &v); err != nil {
		return model.ManualNode{}, fmt.Errorf("invalid VMess JSON: %w", err)
	}
	port, err := jsonIntOrString(v.Port)
	if err != nil || port < 1 || port > 65535 {
		return model.ManualNode{}, errors.New("invalid VMess port")
	}
	aid, _ := jsonIntOrString(v.Aid)
	cipher := strings.TrimSpace(v.Scy)
	if cipher == "" {
		cipher = "auto"
	}
	netw := normalizeNetwork(v.Net)
	n := model.ManualNode{
		Name:              strings.TrimSpace(v.PS),
		Type:              model.ProxyTypeVMess,
		Server:            strings.TrimSpace(v.Add),
		Port:              port,
		UUID:              strings.TrimSpace(v.ID),
		AlterID:           aid,
		Cipher:            cipher,
		UDP:               true,
		TLS:               jsonTLSBool(v.TLS),
		SNI:               strings.TrimSpace(v.SNI),
		ClientFingerprint: strings.TrimSpace(v.FP),
		ALPN:              splitALPN(v.ALPN),
		Network:           netw,
	}
	switch netw {
	case "ws":
		n.WSPath = strings.TrimSpace(v.Path)
		n.WSHost = strings.TrimSpace(v.Host)
	case "grpc":
		n.GRPCServiceName = strings.TrimSpace(v.Path)
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseVLESSURI(rest string) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	uuid, host, port, hasUser, err := splitUserHostPort(beforeQuery, 0)
	if err != nil || !hasUser {
		return model.ManualNode{}, errors.New("invalid VLESS URI")
	}
	sec := strings.ToLower(queryFirst(q, "security"))
	netw := normalizeNetwork(queryFirst(q, "type"))
	pbk := queryFirst(q, "pbk")
	n := model.ManualNode{
		Name:              name,
		Type:              model.ProxyTypeVLESS,
		Server:            host,
		Port:              port,
		UUID:              pathUnescape(uuid),
		Flow:              queryFirst(q, "flow"),
		PacketEncoding:    queryFirst(q, "packetEncoding", "packet-encoding"),
		UDP:               true,
		TLS:               sec == "tls" || sec == "reality" || pbk != "",
		SNI:               queryFirst(q, "sni", "peer"),
		SkipCertVerify:    queryBool(q, "allowInsecure", "insecure", "skip-cert-verify"),
		ClientFingerprint: queryFirst(q, "fp"),
		ALPN:              splitALPN(queryFirst(q, "alpn")),
		RealityPublicKey:  pbk,
		RealityShortID:    queryFirst(q, "sid"),
		Network:           netw,
	}
	switch netw {
	case "ws":
		n.WSPath = queryFirst(q, "path")
		n.WSHost = queryFirst(q, "host")
	case "grpc":
		n.GRPCServiceName = queryFirst(q, "serviceName", "path")
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseTrojanURI(rest string, isGo bool) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	password, host, port, hasUser, err := splitUserHostPort(beforeQuery, 0)
	if err != nil || !hasUser {
		return model.ManualNode{}, errors.New("invalid Trojan URI")
	}
	typ := model.ProxyTypeTrojan
	if isGo {
		typ = model.ProxyTypeTrojanGo
	}
	netw := normalizeNetwork(queryFirst(q, "type"))
	n := model.ManualNode{
		Name:              name,
		Type:              typ,
		Server:            host,
		Port:              port,
		Password:          pathUnescape(password),
		UDP:               true,
		TLS:               true,
		SNI:               queryFirst(q, "sni", "peer"),
		SkipCertVerify:    queryBool(q, "allowInsecure", "insecure", "skip-cert-verify"),
		ClientFingerprint: queryFirst(q, "fp"),
		ALPN:              splitALPN(queryFirst(q, "alpn")),
		Network:           netw,
	}
	switch netw {
	case "ws":
		n.WSPath = queryFirst(q, "path")
		n.WSHost = queryFirst(q, "host")
	case "grpc":
		n.GRPCServiceName = queryFirst(q, "serviceName", "path")
	}
	if isGo {
		if enc := queryFirst(q, "encryption"); enc != "" {
			if parts := strings.SplitN(enc, ";", 3); len(parts) == 3 && strings.EqualFold(strings.TrimSpace(parts[0]), "ss") {
				n.SSOptsEnabled = true
				n.SSOptsMethod = strings.TrimSpace(parts[1])
				n.SSOptsPassword = parts[2]
			}
		}
	}
	applyURINameFallback(&n)
	return n, nil
}

func formatBandwidthMbps(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if _, err := strconv.Atoi(v); err == nil {
		return v + " Mbps"
	}
	return v
}

func parseHysteria2URI(rest string) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	password, host, port, hasUser, err := splitUserHostPort(beforeQuery, 443)
	if err != nil || !hasUser {
		return model.ManualNode{}, errors.New("invalid Hysteria2 URI")
	}
	n := model.ManualNode{
		Name:           name,
		Type:           model.ProxyTypeHysteria2,
		Server:         host,
		Port:           port,
		Password:       pathUnescape(password),
		Ports:          queryFirst(q, "mport", "ports"),
		Up:             formatBandwidthMbps(queryFirst(q, "up", "upmbps")),
		Down:           formatBandwidthMbps(queryFirst(q, "down", "downmbps")),
		Obfs:           queryFirst(q, "obfs"),
		ObfsPassword:   queryFirst(q, "obfs-password"),
		SNI:            queryFirst(q, "sni", "peer"),
		SkipCertVerify: queryBool(q, "insecure", "allowInsecure", "skip-cert-verify"),
		ALPN:           splitALPN(queryFirst(q, "alpn")),
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseHysteriaURI(rest string) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	_, host, port, _, err := splitUserHostPort(beforeQuery, 443)
	if err != nil {
		return model.ManualNode{}, errors.New("invalid Hysteria URI")
	}
	proto := queryFirst(q, "protocol")
	if proto == "" {
		proto = "udp"
	}
	n := model.ManualNode{
		Name:           name,
		Type:           model.ProxyTypeHysteria,
		Server:         host,
		Port:           port,
		AuthStr:        queryFirst(q, "auth", "auth-str"),
		Protocol:       proto,
		Up:             formatBandwidthMbps(queryFirst(q, "up", "upmbps")),
		Down:           formatBandwidthMbps(queryFirst(q, "down", "downmbps")),
		Obfs:           queryFirst(q, "obfsParam", "obfs"),
		SNI:            queryFirst(q, "peer", "sni"),
		SkipCertVerify: queryBool(q, "insecure", "allowInsecure", "skip-cert-verify"),
		ALPN:           splitALPN(queryFirst(q, "alpn")),
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseTUICURI(rest string) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	userinfo, host, port, hasUser, err := splitUserHostPort(beforeQuery, 443)
	if err != nil || !hasUser {
		return model.ManualNode{}, errors.New("invalid TUIC URI")
	}
	n := model.ManualNode{
		Name:                 name,
		Type:                 model.ProxyTypeTUIC,
		Server:               host,
		Port:                 port,
		CongestionController: queryFirst(q, "congestion_control", "congestion-controller"),
		UDPRelayMode:         queryFirst(q, "udp_relay_mode", "udp-relay-mode"),
		SNI:                  queryFirst(q, "sni", "peer"),
		SkipCertVerify:       queryBool(q, "allow_insecure", "insecure", "allowInsecure", "skip-cert-verify"),
		ALPN:                 splitALPN(queryFirst(q, "alpn")),
		ReduceRTT:            queryBool(q, "reduce_rtt", "reduce-rtt"),
		DisableSNI:           queryBool(q, "disable_sni", "disable-sni"),
	}
	if uuid, pass, ok := strings.Cut(userinfo, ":"); ok {
		n.UUID = pathUnescape(uuid)
		n.Password = pathUnescape(pass)
	} else {
		n.Token = pathUnescape(userinfo)
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseAnyTLSURI(rest string) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	password, host, port, hasUser, err := splitUserHostPort(beforeQuery, 443)
	if err != nil || !hasUser {
		return model.ManualNode{}, errors.New("invalid AnyTLS URI")
	}
	ic, _ := strconv.Atoi(queryFirst(q, "idle_session_check_interval", "idle-session-check-interval"))
	it, _ := strconv.Atoi(queryFirst(q, "idle_session_timeout", "idle-session-timeout"))
	im, _ := strconv.Atoi(queryFirst(q, "min_idle_session", "min-idle-session"))
	n := model.ManualNode{
		Name:                     name,
		Type:                     model.ProxyTypeAnyTLS,
		Server:                   host,
		Port:                     port,
		Password:                 pathUnescape(password),
		UDP:                      true,
		TLS:                      true,
		SNI:                      queryFirst(q, "sni", "peer"),
		SkipCertVerify:           queryBool(q, "insecure", "allowInsecure", "skip-cert-verify"),
		ClientFingerprint:        queryFirst(q, "fp"),
		ALPN:                     splitALPN(queryFirst(q, "alpn")),
		IdleSessionCheckInterval: ic,
		IdleSessionTimeout:       it,
		MinIdleSession:           im,
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseSOCKSURI(rest string, tls bool) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	target := beforeQuery
	if !strings.Contains(target, "@") {
		trimmed := strings.TrimSuffix(strings.TrimSpace(target), "/")
		if dec, err := decodeBase64Flex(trimmed); err == nil && strings.Contains(string(dec), "@") {
			target = string(dec)
		}
	}
	userinfo, host, port, hasUser, err := splitUserHostPort(target, 1080)
	if err != nil {
		return model.ManualNode{}, errors.New("invalid SOCKS5 URI")
	}
	n := model.ManualNode{
		Name:           name,
		Type:           model.ProxyTypeSOCKS5,
		Server:         host,
		Port:           port,
		UDP:            true,
		TLS:            tls || queryBool(q, "tls"),
		SkipCertVerify: queryBool(q, "insecure", "allowInsecure", "skip-cert-verify"),
	}
	if hasUser {
		if user, pass, ok := strings.Cut(userinfo, ":"); ok {
			n.Username = pathUnescape(user)
			n.Password = pathUnescape(pass)
		} else {
			n.Username = pathUnescape(userinfo)
		}
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseHTTPURI(rest string, tls bool) (model.ManualNode, error) {
	beforeFrag, name := splitFragment(rest)
	beforeQuery, q, err := splitQuery(beforeFrag)
	if err != nil {
		return model.ManualNode{}, err
	}
	defaultPort := 80
	if tls {
		defaultPort = 443
	}
	userinfo, host, port, hasUser, err := splitUserHostPort(beforeQuery, defaultPort)
	if err != nil {
		return model.ManualNode{}, errors.New("invalid HTTP URI")
	}
	n := model.ManualNode{
		Name:           name,
		Type:           model.ProxyTypeHTTP,
		Server:         host,
		Port:           port,
		TLS:            tls || queryBool(q, "tls"),
		SNI:            queryFirst(q, "sni", "peer"),
		SkipCertVerify: queryBool(q, "insecure", "allowInsecure", "skip-cert-verify"),
	}
	if hasUser {
		if user, pass, ok := strings.Cut(userinfo, ":"); ok {
			n.Username = pathUnescape(user)
			n.Password = pathUnescape(pass)
		} else {
			n.Username = pathUnescape(userinfo)
		}
	}
	applyURINameFallback(&n)
	return n, nil
}

func parseYAMLNodeSnippet(text string) (model.ManualNode, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return model.ManualNode{}, false, nil
	}
	if len(doc.Content) == 0 {
		return model.ManualNode{}, false, nil
	}
	root := deref(doc.Content[0])
	if root == nil {
		return model.ManualNode{}, false, nil
	}

	var m *yaml.Node
	switch root.Kind {
	case yaml.MappingNode:
		if ps := deref(mapGet(root, "proxies")); ps != nil {
			if ps.Kind == yaml.SequenceNode && len(ps.Content) > 0 {
				m = deref(ps.Content[0])
			} else {
				return model.ManualNode{}, true, errors.New("empty proxies list in YAML")
			}
		} else {
			m = root
		}
	case yaml.SequenceNode:
		if len(root.Content) > 0 {
			m = deref(root.Content[0])
		}
	default:
		return model.ManualNode{}, false, nil
	}

	if m == nil || m.Kind != yaml.MappingNode {
		return model.ManualNode{}, false, nil
	}
	if mapGet(m, "type") == nil && mapGet(m, "server") == nil {
		return model.ManualNode{}, false, nil
	}

	rawType := strings.ToLower(strings.TrimSpace(yamlStr(m, "type")))
	var typ string
	switch rawType {
	case "socks5", "socks":
		typ = model.ProxyTypeSOCKS5
	case "http":
		typ = model.ProxyTypeHTTP
	case "ss", "shadowsocks":
		typ = model.ProxyTypeSS
	case "vmess":
		typ = model.ProxyTypeVMess
	case "vless":
		typ = model.ProxyTypeVLESS
	case "trojan":
		typ = model.ProxyTypeTrojan
		if ssOpts := deref(mapGet(m, "ss-opts")); ssOpts != nil && yamlBool(ssOpts, "enabled") {
			typ = model.ProxyTypeTrojanGo
		}
	case "trojan-go":
		typ = model.ProxyTypeTrojanGo
	case "hysteria", "hy":
		typ = model.ProxyTypeHysteria
	case "hysteria2", "hy2":
		typ = model.ProxyTypeHysteria2
	case "tuic":
		typ = model.ProxyTypeTUIC
	case "anytls":
		typ = model.ProxyTypeAnyTLS
	default:
		return model.ManualNode{}, true, fmt.Errorf("unsupported proxy type %q", rawType)
	}

	server := strings.TrimSpace(yamlStr(m, "server"))
	if server == "" {
		return model.ManualNode{}, true, errors.New("missing server in YAML")
	}
	port := yamlInt(m, "port")
	if port < 1 || port > 65535 {
		return model.ManualNode{}, true, errors.New("port must be between 1 and 65535")
	}

	sni := yamlStr(m, "sni")
	if sni == "" {
		sni = yamlStr(m, "servername")
	}
	tls := yamlBool(m, "tls")
	if typ == model.ProxyTypeTrojan || typ == model.ProxyTypeTrojanGo || typ == model.ProxyTypeAnyTLS {
		tls = true
	}

	n := model.ManualNode{
		Name:                     yamlStr(m, "name"),
		Type:                     typ,
		Server:                   server,
		Port:                     port,
		UDP:                      yamlBool(m, "udp"),
		Username:                 yamlStr(m, "username"),
		Password:                 yamlStr(m, "password"),
		UUID:                     yamlStr(m, "uuid"),
		Token:                    yamlStr(m, "token"),
		AuthStr:                  yamlStr(m, "auth-str"),
		Cipher:                   yamlStr(m, "cipher"),
		AlterID:                  yamlInt(m, "alterId"),
		Flow:                     yamlStr(m, "flow"),
		PacketEncoding:           yamlStr(m, "packet-encoding"),
		TLS:                      tls,
		SNI:                      sni,
		SkipCertVerify:           yamlBool(m, "skip-cert-verify"),
		ClientFingerprint:        yamlStr(m, "client-fingerprint"),
		ALPN:                     yamlStrSlice(m, "alpn"),
		Network:                  normalizeNetwork(yamlStr(m, "network")),
		Plugin:                   yamlStr(m, "plugin"),
		Protocol:                 yamlStr(m, "protocol"),
		Ports:                    yamlStr(m, "ports"),
		Up:                       yamlStr(m, "up"),
		Down:                     yamlStr(m, "down"),
		Obfs:                     yamlStr(m, "obfs"),
		ObfsPassword:             yamlStr(m, "obfs-password"),
		CongestionController:     yamlStr(m, "congestion-controller"),
		UDPRelayMode:             yamlStr(m, "udp-relay-mode"),
		ReduceRTT:                yamlBool(m, "reduce-rtt"),
		DisableSNI:               yamlBool(m, "disable-sni"),
		IdleSessionCheckInterval: yamlInt(m, "idle-session-check-interval"),
		IdleSessionTimeout:       yamlInt(m, "idle-session-timeout"),
		MinIdleSession:           yamlInt(m, "min-idle-session"),
	}
	if n.Network == "tcp" && yamlStr(m, "network") == "" {
		n.Network = ""
	}
	if ro := deref(mapGet(m, "reality-opts")); ro != nil {
		n.RealityPublicKey = yamlStr(ro, "public-key")
		n.RealityShortID = yamlStr(ro, "short-id")
		if n.RealityPublicKey != "" {
			n.TLS = true
		}
	}
	if ws := deref(mapGet(m, "ws-opts")); ws != nil {
		n.WSPath = yamlStr(ws, "path")
		if hdrs := deref(mapGet(ws, "headers")); hdrs != nil {
			n.WSHost = yamlStr(hdrs, "Host")
			if n.WSHost == "" {
				n.WSHost = yamlStr(hdrs, "host")
			}
		}
	}
	if grpc := deref(mapGet(m, "grpc-opts")); grpc != nil {
		n.GRPCServiceName = yamlStr(grpc, "grpc-service-name")
	}
	if po := deref(mapGet(m, "plugin-opts")); po != nil {
		n.PluginMode = yamlStr(po, "mode")
		n.PluginHost = yamlStr(po, "host")
	}
	if sso := deref(mapGet(m, "ss-opts")); sso != nil {
		n.SSOptsEnabled = yamlBool(sso, "enabled")
		n.SSOptsMethod = yamlStr(sso, "method")
		n.SSOptsPassword = yamlStr(sso, "password")
	}
	applyURINameFallback(&n)
	return n, true, nil
}

func yamlStr(m *yaml.Node, key string) string {
	v := deref(mapGet(m, key))
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(v.Value)
}

func yamlInt(m *yaml.Node, key string) int {
	v := deref(mapGet(m, key))
	if v == nil || v.Kind != yaml.ScalarNode {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(v.Value))
	return n
}

func yamlBool(m *yaml.Node, key string) bool {
	v := deref(mapGet(m, key))
	if v == nil || v.Kind != yaml.ScalarNode {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v.Value)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

func yamlStrSlice(m *yaml.Node, key string) []string {
	v := deref(mapGet(m, key))
	if v == nil {
		return nil
	}
	switch v.Kind {
	case yaml.SequenceNode:
		var out []string
		for _, item := range v.Content {
			s := deref(item)
			if s != nil && s.Kind == yaml.ScalarNode {
				if val := strings.TrimSpace(s.Value); val != "" {
					out = append(out, val)
				}
			}
		}
		return out
	case yaml.ScalarNode:
		return splitALPN(v.Value)
	default:
		return nil
	}
}
