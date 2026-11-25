package profile

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

// ManualGroupName is the select proxy-group created for manually added nodes.
const ManualGroupName = "手动节点"

const layerManualNodes = "手动节点"

var supportedProxyTypes = map[string]bool{
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

// ValidateManualNode normalizes whitespace/defaults and validates n against
// Mihomo's supported outbound proxy schemas.
func ValidateManualNode(n model.ManualNode) (model.ManualNode, error) {
	n.Name = strings.TrimSpace(n.Name)
	n.Type = strings.ToLower(strings.TrimSpace(n.Type))
	n.Server = strings.TrimSpace(n.Server)
	n.Username = strings.TrimSpace(n.Username)
	n.Password = strings.TrimSpace(n.Password)
	n.UUID = strings.TrimSpace(n.UUID)
	n.Token = strings.TrimSpace(n.Token)
	n.AuthStr = strings.TrimSpace(n.AuthStr)
	n.Cipher = strings.TrimSpace(n.Cipher)
	n.Flow = strings.TrimSpace(n.Flow)
	n.PacketEncoding = strings.TrimSpace(n.PacketEncoding)
	n.SNI = strings.TrimSpace(n.SNI)
	n.ClientFingerprint = strings.TrimSpace(n.ClientFingerprint)
	n.RealityPublicKey = strings.TrimSpace(n.RealityPublicKey)
	n.RealityShortID = strings.TrimSpace(n.RealityShortID)
	n.Network = strings.ToLower(strings.TrimSpace(n.Network))
	n.WSPath = strings.TrimSpace(n.WSPath)
	n.WSHost = strings.TrimSpace(n.WSHost)
	n.GRPCServiceName = strings.TrimSpace(n.GRPCServiceName)
	n.Plugin = strings.TrimSpace(n.Plugin)
	n.PluginMode = strings.TrimSpace(n.PluginMode)
	n.PluginHost = strings.TrimSpace(n.PluginHost)
	n.SSOptsMethod = strings.TrimSpace(n.SSOptsMethod)
	n.SSOptsPassword = strings.TrimSpace(n.SSOptsPassword)
	n.Protocol = strings.TrimSpace(n.Protocol)
	n.Ports = strings.TrimSpace(n.Ports)
	n.Up = strings.TrimSpace(n.Up)
	n.Down = strings.TrimSpace(n.Down)
	n.Obfs = strings.TrimSpace(n.Obfs)
	n.ObfsPassword = strings.TrimSpace(n.ObfsPassword)
	n.CongestionController = strings.TrimSpace(n.CongestionController)
	n.UDPRelayMode = strings.TrimSpace(n.UDPRelayMode)

	var alpn []string
	for _, a := range n.ALPN {
		if s := strings.TrimSpace(a); s != "" {
			alpn = append(alpn, s)
		}
	}
	n.ALPN = alpn

	if n.Name == "" {
		return n, fmt.Errorf("节点名称不能为空")
	}
	if !supportedProxyTypes[n.Type] {
		return n, fmt.Errorf("不支持的代理协议「%s」", n.Type)
	}
	if n.Server == "" {
		return n, fmt.Errorf("服务器地址不能为空")
	}
	if n.Port < 1 || n.Port > 65535 {
		return n, fmt.Errorf("端口须为 1–65535 之间的数字")
	}

	switch n.Type {
	case model.ProxyTypeSS:
		if n.Cipher == "" {
			return n, fmt.Errorf("Shadowsocks 节点必须填写加密方式（cipher）")
		}
		if n.Password == "" {
			return n, fmt.Errorf("Shadowsocks 节点必须填写密码")
		}
	case model.ProxyTypeVMess:
		if n.UUID == "" {
			return n, fmt.Errorf("VMess 节点必须填写 UUID")
		}
		if n.Cipher == "" {
			n.Cipher = "auto"
		}
	case model.ProxyTypeVLESS:
		if n.UUID == "" {
			return n, fmt.Errorf("VLESS 节点必须填写 UUID")
		}
	case model.ProxyTypeTrojan:
		if n.Password == "" {
			return n, fmt.Errorf("Trojan 节点必须填写密码")
		}
	case model.ProxyTypeTrojanGo:
		if n.Password == "" {
			return n, fmt.Errorf("Trojan Go 节点必须填写密码")
		}
		if n.SSOptsEnabled {
			if n.SSOptsMethod == "" {
				n.SSOptsMethod = "aes-128-gcm"
			}
			if n.SSOptsPassword == "" {
				return n, fmt.Errorf("开启 Trojan Go Shadowsocks 二次加密时必须填写 SS 密码")
			}
		}
	case model.ProxyTypeHysteria:
		if n.Up == "" || n.Down == "" {
			return n, fmt.Errorf("Hysteria 节点必须填写上行和下行带宽（例如 50 Mbps）")
		}
	case model.ProxyTypeHysteria2:
		if n.Password == "" {
			return n, fmt.Errorf("Hysteria2 节点必须填写密码")
		}
	case model.ProxyTypeTUIC:
		if n.Token == "" && (n.UUID == "" || n.Password == "") {
			return n, fmt.Errorf("TUIC 节点须填写 UUID 与密码（v5），或填写 Token（v4）")
		}
	case model.ProxyTypeAnyTLS:
		if n.Password == "" {
			return n, fmt.Errorf("AnyTLS 节点必须填写密码")
		}
	}
	return n, nil
}

// BuildManualProxyNode validates n and builds its Mihomo YAML proxy mapping.
func BuildManualProxyNode(n model.ManualNode) (*yaml.Node, error) {
	var err error
	n, err = ValidateManualNode(n)
	if err != nil {
		return nil, err
	}

	outType := n.Type
	if n.Type == model.ProxyTypeTrojanGo {
		outType = model.ProxyTypeTrojan
	}
	m := mapNode(
		"name", strNode(n.Name),
		"type", strNode(outType),
		"server", strNode(n.Server),
		"port", intNode(n.Port),
	)

	switch n.Type {
	case model.ProxyTypeSOCKS5:
		setNonEmpty(m, "username", n.Username)
		setNonEmpty(m, "password", n.Password)
		if n.TLS {
			mapSet(m, "tls", boolNode(true))
		}
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}
		mapSet(m, "udp", boolNode(n.UDP))

	case model.ProxyTypeHTTP:
		setNonEmpty(m, "username", n.Username)
		setNonEmpty(m, "password", n.Password)
		if n.TLS {
			mapSet(m, "tls", boolNode(true))
			setNonEmpty(m, "sni", n.SNI)
		}
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}

	case model.ProxyTypeSS:
		mapSet(m, "cipher", strNode(n.Cipher))
		mapSet(m, "password", strNode(n.Password))
		mapSet(m, "udp", boolNode(n.UDP))
		if n.Plugin != "" {
			mapSet(m, "plugin", strNode(n.Plugin))
			opts := mapNode()
			setNonEmpty(opts, "mode", n.PluginMode)
			setNonEmpty(opts, "host", n.PluginHost)
			if len(opts.Content) > 0 {
				mapSet(m, "plugin-opts", opts)
			}
		}

	case model.ProxyTypeVMess:
		mapSet(m, "uuid", strNode(n.UUID))
		mapSet(m, "alterId", intNode(n.AlterID))
		mapSet(m, "cipher", strNode(n.Cipher))
		mapSet(m, "udp", boolNode(n.UDP))
		if n.TLS {
			mapSet(m, "tls", boolNode(true))
			setNonEmpty(m, "servername", n.SNI)
			if n.SkipCertVerify {
				mapSet(m, "skip-cert-verify", boolNode(true))
			}
			setNonEmpty(m, "client-fingerprint", n.ClientFingerprint)
		}
		applyTransport(m, n)

	case model.ProxyTypeVLESS:
		mapSet(m, "uuid", strNode(n.UUID))
		mapSet(m, "udp", boolNode(n.UDP))
		setNonEmpty(m, "flow", n.Flow)
		setNonEmpty(m, "packet-encoding", n.PacketEncoding)
		if n.TLS || n.RealityPublicKey != "" {
			mapSet(m, "tls", boolNode(true))
		}
		setNonEmpty(m, "servername", n.SNI)
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}
		setNonEmpty(m, "client-fingerprint", n.ClientFingerprint)
		if n.RealityPublicKey != "" {
			ro := mapNode("public-key", strNode(n.RealityPublicKey))
			setNonEmpty(ro, "short-id", n.RealityShortID)
			mapSet(m, "reality-opts", ro)
		}
		applyTransport(m, n)

	case model.ProxyTypeTrojan, model.ProxyTypeTrojanGo:
		mapSet(m, "password", strNode(n.Password))
		mapSet(m, "udp", boolNode(n.UDP))
		setNonEmpty(m, "sni", n.SNI)
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}
		setNonEmpty(m, "client-fingerprint", n.ClientFingerprint)
		if len(n.ALPN) > 0 {
			mapSet(m, "alpn", strSeq(n.ALPN))
		}
		applyTransport(m, n)
		if n.SSOptsEnabled {
			method := n.SSOptsMethod
			if method == "" {
				method = "aes-128-gcm"
			}
			mapSet(m, "ss-opts", mapNode(
				"enabled", boolNode(true),
				"method", strNode(method),
				"password", strNode(n.SSOptsPassword),
			))
		}

	case model.ProxyTypeHysteria:
		mapSet(m, "up", strNode(n.Up))
		mapSet(m, "down", strNode(n.Down))
		setNonEmpty(m, "auth-str", n.AuthStr)
		setNonEmpty(m, "protocol", n.Protocol)
		setNonEmpty(m, "obfs", n.Obfs)
		setNonEmpty(m, "sni", n.SNI)
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}
		if len(n.ALPN) > 0 {
			mapSet(m, "alpn", strSeq(n.ALPN))
		}

	case model.ProxyTypeHysteria2:
		mapSet(m, "password", strNode(n.Password))
		setNonEmpty(m, "ports", n.Ports)
		setNonEmpty(m, "up", n.Up)
		setNonEmpty(m, "down", n.Down)
		if n.Obfs != "" {
			mapSet(m, "obfs", strNode(n.Obfs))
			setNonEmpty(m, "obfs-password", n.ObfsPassword)
		}
		setNonEmpty(m, "sni", n.SNI)
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}
		if len(n.ALPN) > 0 {
			mapSet(m, "alpn", strSeq(n.ALPN))
		}

	case model.ProxyTypeTUIC:
		if n.Token != "" {
			mapSet(m, "token", strNode(n.Token))
		} else {
			mapSet(m, "uuid", strNode(n.UUID))
			mapSet(m, "password", strNode(n.Password))
		}
		setNonEmpty(m, "congestion-controller", n.CongestionController)
		setNonEmpty(m, "udp-relay-mode", n.UDPRelayMode)
		setNonEmpty(m, "sni", n.SNI)
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}
		if len(n.ALPN) > 0 {
			mapSet(m, "alpn", strSeq(n.ALPN))
		}
		if n.ReduceRTT {
			mapSet(m, "reduce-rtt", boolNode(true))
		}
		if n.DisableSNI {
			mapSet(m, "disable-sni", boolNode(true))
		}

	case model.ProxyTypeAnyTLS:
		mapSet(m, "password", strNode(n.Password))
		mapSet(m, "udp", boolNode(n.UDP))
		setNonEmpty(m, "sni", n.SNI)
		if n.SkipCertVerify {
			mapSet(m, "skip-cert-verify", boolNode(true))
		}
		setNonEmpty(m, "client-fingerprint", n.ClientFingerprint)
		if len(n.ALPN) > 0 {
			mapSet(m, "alpn", strSeq(n.ALPN))
		}
		if n.IdleSessionCheckInterval > 0 {
			mapSet(m, "idle-session-check-interval", intNode(n.IdleSessionCheckInterval))
		}
		if n.IdleSessionTimeout > 0 {
			mapSet(m, "idle-session-timeout", intNode(n.IdleSessionTimeout))
		}
		if n.MinIdleSession > 0 {
			mapSet(m, "min-idle-session", intNode(n.MinIdleSession))
		}
	}

	return m, nil
}

func setNonEmpty(m *yaml.Node, key, val string) {
	if val != "" {
		mapSet(m, key, strNode(val))
	}
}

func applyTransport(m *yaml.Node, n model.ManualNode) {
	switch n.Network {
	case "ws":
		mapSet(m, "network", strNode("ws"))
		ws := mapNode()
		setNonEmpty(ws, "path", n.WSPath)
		if n.WSHost != "" {
			mapSet(ws, "headers", mapNode("Host", strNode(n.WSHost)))
		}
		if len(ws.Content) > 0 {
			mapSet(m, "ws-opts", ws)
		}
	case "grpc":
		mapSet(m, "network", strNode("grpc"))
		if n.GRPCServiceName != "" {
			mapSet(m, "grpc-opts", mapNode("grpc-service-name", strNode(n.GRPCServiceName)))
		}
	case "tcp":
		mapSet(m, "network", strNode("tcp"))
	}
}

// addManualNodes appends user-defined nodes into `proxies`, creates/updates
// the "手动节点" select group in `proxy-groups`, and links it into the first
// existing non-hidden select group when present.
func (c *composer) addManualNodes(nodes []model.ManualNode) error {
	if len(nodes) == 0 {
		return nil
	}
	existing := map[string]bool{}
	for _, name := range namesOf(mapGet(c.root, "proxies")) {
		existing[name] = true
	}

	built := make([]*yaml.Node, 0, len(nodes))
	names := make([]string, 0, len(nodes))
	seen := map[string]bool{}
	for _, n := range nodes {
		y, err := BuildManualProxyNode(n)
		if err != nil {
			return fmt.Errorf("手动节点「%s」无效：%w", strings.TrimSpace(n.Name), err)
		}
		name := deref(mapGet(y, "name")).Value
		if seen[name] {
			return fmt.Errorf("手动节点名称「%s」重复", name)
		}
		if existing[name] {
			return fmt.Errorf("手动节点名称「%s」与订阅中的节点重名", name)
		}
		seen[name] = true
		built = append(built, c.mark(y))
		names = append(names, name)
	}

	ps := c.seq("proxies")
	ps.Content = append(ps.Content, built...)

	gs := c.seq("proxy-groups")
	var manualGroup *yaml.Node
	var firstSelect *yaml.Node
	for _, item := range gs.Content {
		gm := deref(item)
		if gm == nil || gm.Kind != yaml.MappingNode {
			continue
		}
		gn := deref(mapGet(gm, "name"))
		gt := deref(mapGet(gm, "type"))
		if gn == nil {
			continue
		}
		if gn.Value == ManualGroupName {
			manualGroup = gm
			continue
		}
		if firstSelect == nil && gn.Value != "GLOBAL" && gn.Value != probeGroupName && gt != nil && gt.Value == "select" {
			hidden := deref(mapGet(gm, "hidden"))
			if hidden == nil || !strings.EqualFold(hidden.Value, "true") {
				firstSelect = gm
			}
		}
	}

	if manualGroup != nil {
		gProxies := deref(mapGet(manualGroup, "proxies"))
		if gProxies == nil || gProxies.Kind != yaml.SequenceNode {
			gProxies = seqNode()
			mapSet(manualGroup, "proxies", gProxies)
		}
		has := seqSet(gProxies)
		for _, name := range names {
			if !has[name] {
				gProxies.Content = append(gProxies.Content, c.mark(strNode(name)))
			}
		}
	} else {
		gs.Content = append(gs.Content, c.mark(mapNode(
			"name", strNode(ManualGroupName),
			"type", strNode("select"),
			"proxies", strSeq(names),
		)))
	}

	if firstSelect != nil {
		if gProxies := deref(mapGet(firstSelect, "proxies")); gProxies != nil && gProxies.Kind == yaml.SequenceNode {
			if !seqSet(gProxies)[ManualGroupName] {
				gProxies.Content = append(gProxies.Content, c.mark(strNode(ManualGroupName)))
			}
		}
	}
	return nil
}

func seqSet(seq *yaml.Node) map[string]bool {
	out := map[string]bool{}
	for _, item := range seq.Content {
		if s := deref(item); s != nil && s.Kind == yaml.ScalarNode {
			out[s.Value] = true
		}
	}
	return out
}
