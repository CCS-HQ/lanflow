package model

// Supported outbound proxy protocol types for manually added nodes.
// All map to native Mihomo outbound types (Trojan Go maps to "trojan" with
// WebSocket/gRPC transport and Shadowsocks AEAD ss-opts).
const (
	ProxyTypeSOCKS5    = "socks5"
	ProxyTypeHTTP      = "http"
	ProxyTypeSS        = "ss"
	ProxyTypeVMess     = "vmess"
	ProxyTypeVLESS     = "vless"
	ProxyTypeTrojan    = "trojan"
	ProxyTypeTrojanGo  = "trojan-go"
	ProxyTypeHysteria  = "hysteria"
	ProxyTypeHysteria2 = "hysteria2"
	ProxyTypeTUIC      = "tuic"
	ProxyTypeAnyTLS    = "anytls"
)

// ManualNode is a user-defined outbound proxy node stored independently of
// imported subscriptions and merged into the composed Mihomo runtime config.
type ManualNode struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Server string `json:"server"`
	Port   int    `json:"port"`
	UDP    bool   `json:"udp,omitempty"`

	// Authentication / identity
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Token    string `json:"token,omitempty"`   // TUIC v4
	AuthStr  string `json:"authStr,omitempty"` // Hysteria v1

	// Cipher / VMess
	Cipher  string `json:"cipher,omitempty"`
	AlterID int    `json:"alterId,omitempty"`

	// VLESS
	Flow           string `json:"flow,omitempty"`
	PacketEncoding string `json:"packetEncoding,omitempty"`

	// TLS / Reality
	TLS               bool     `json:"tls,omitempty"`
	SNI               string   `json:"sni,omitempty"`
	SkipCertVerify    bool     `json:"skipCertVerify,omitempty"`
	ClientFingerprint string   `json:"clientFingerprint,omitempty"`
	ALPN              []string `json:"alpn,omitempty"`
	RealityPublicKey  string   `json:"realityPublicKey,omitempty"`
	RealityShortID    string   `json:"realityShortId,omitempty"`

	// Transport (VMess / VLESS / Trojan / Trojan Go)
	Network         string `json:"network,omitempty"` // "", "tcp", "ws", "grpc"
	WSPath          string `json:"wsPath,omitempty"`
	WSHost          string `json:"wsHost,omitempty"`
	GRPCServiceName string `json:"grpcServiceName,omitempty"`

	// Shadowsocks plugin
	Plugin     string `json:"plugin,omitempty"`
	PluginMode string `json:"pluginMode,omitempty"`
	PluginHost string `json:"pluginHost,omitempty"`

	// Trojan Go compatibility (emits ss-opts on Mihomo's trojan outbound)
	SSOptsEnabled  bool   `json:"ssOptsEnabled,omitempty"`
	SSOptsMethod   string `json:"ssOptsMethod,omitempty"`
	SSOptsPassword string `json:"ssOptsPassword,omitempty"`

	// Hysteria / Hysteria2
	Protocol     string `json:"protocol,omitempty"` // Hysteria v1: udp / wechat-video / faketcp
	Ports        string `json:"ports,omitempty"`    // Hysteria2 port hopping range
	Up           string `json:"up,omitempty"`
	Down         string `json:"down,omitempty"`
	Obfs         string `json:"obfs,omitempty"`
	ObfsPassword string `json:"obfsPassword,omitempty"`

	// TUIC
	CongestionController string `json:"congestionController,omitempty"`
	UDPRelayMode         string `json:"udpRelayMode,omitempty"`
	ReduceRTT            bool   `json:"reduceRtt,omitempty"`
	DisableSNI           bool   `json:"disableSni,omitempty"`

	// AnyTLS idle session tuning
	IdleSessionCheckInterval int `json:"idleSessionCheckInterval,omitempty"`
	IdleSessionTimeout       int `json:"idleSessionTimeout,omitempty"`
	MinIdleSession           int `json:"minIdleSession,omitempty"`
}
