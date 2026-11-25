package proxies

import (
	"errors"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
	"lanflow/internal/view/env"
)

type protoOption struct {
	typ         string
	label       string
	defaultPort string
}

var protoOptions = []protoOption{
	{typ: model.ProxyTypeSOCKS5, label: "SOCKS5", defaultPort: "1080"},
	{typ: model.ProxyTypeHTTP, label: "HTTP", defaultPort: "8080"},
	{typ: model.ProxyTypeSS, label: "Shadowsocks", defaultPort: "8388"},
	{typ: model.ProxyTypeVMess, label: "VMess", defaultPort: "443"},
	{typ: model.ProxyTypeVLESS, label: "VLESS", defaultPort: "443"},
	{typ: model.ProxyTypeTrojan, label: "Trojan", defaultPort: "443"},
	{typ: model.ProxyTypeTrojanGo, label: "Trojan Go", defaultPort: "443"},
	{typ: model.ProxyTypeHysteria2, label: "Hysteria2", defaultPort: "443"},
	{typ: model.ProxyTypeHysteria, label: "Hysteria", defaultPort: "443"},
	{typ: model.ProxyTypeTUIC, label: "TUIC", defaultPort: "443"},
	{typ: model.ProxyTypeAnyTLS, label: "AnyTLS", defaultPort: "443"},
}

var protoLabels = func() []string {
	out := make([]string, len(protoOptions))
	for i, o := range protoOptions {
		out[i] = o.label
	}
	return out
}()

var (
	networkOptions     = []string{"tcp", "ws", "grpc"}
	congestionOptions  = []string{"bbr", "cubic", "reno"}
	udpRelayOptions    = []string{"native", "quic"}
	hysteriaProtoOpts  = []string{"udp", "wechat-video", "faketcp"}
	ssOptsMethodChoice = []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"}
)

type manualNodeDialog struct {
	open    bool
	session int
	saving  bool
	saved   bool
	err     string

	id         string
	typ        string
	protoLabel string
	name       string
	server     string
	port       string
	udp        bool

	username string
	password string
	uuid     string
	token    string
	authStr  string

	cipher  string
	alterID string

	flow           string
	packetEncoding string

	tls               bool
	sni               string
	skipCertVerify    bool
	clientFingerprint string
	alpn              string
	realityPublicKey  string
	realityShortID    string

	network         string
	wsPath          string
	wsHost          string
	grpcServiceName string

	plugin     string
	pluginMode string
	pluginHost string

	ssOptsEnabled  bool
	ssOptsMethod   string
	ssOptsPassword string

	hyProtocol   string
	ports        string
	up           string
	down         string
	obfs         string
	obfsPassword string

	congestion   string
	udpRelayMode string
	reduceRTT    bool
	disableSNI   bool

	idleCheck   string
	idleTimeout string
	minIdle     string

	advanced bool
}

func protoOptionForType(typ string) protoOption {
	for _, o := range protoOptions {
		if o.typ == typ {
			return o
		}
	}
	return protoOptions[0]
}

func protoOptionForLabel(label string) protoOption {
	for _, o := range protoOptions {
		if o.label == label {
			return o
		}
	}
	return protoOptions[0]
}

func defaultManualNodeForType(typ string) model.ManualNode {
	opt := protoOptionForType(typ)
	port, _ := strconv.Atoi(opt.defaultPort)
	n := model.ManualNode{
		Type: opt.typ,
		Port: port,
	}
	switch opt.typ {
	case model.ProxyTypeSOCKS5:
		n.UDP = true
	case model.ProxyTypeSS:
		n.Cipher = "aes-128-gcm"
		n.UDP = true
	case model.ProxyTypeVMess:
		n.Cipher = "auto"
		n.UDP = true
	case model.ProxyTypeVLESS:
		n.TLS = true
		n.UDP = true
	case model.ProxyTypeTrojan, model.ProxyTypeTrojanGo:
		n.TLS = true
		n.UDP = true
	case model.ProxyTypeAnyTLS:
		n.TLS = true
		n.UDP = true
	}
	return n
}

func (p *Page) openAddManualNodeOfType(typ string) {
	p.openManualNode(defaultManualNodeForType(typ))
}

func (p *Page) importManualNodeFromClipboard(c *ui.Context) {
	node, err := profile.ParseManualNodeClipboard(c.ReadClipboard())
	if err != nil {
		switch {
		case errors.Is(err, profile.ErrClipboardEmpty):
			p.e.Toast(i18n.T("剪贴板为空"), true)
		case errors.Is(err, profile.ErrUnsupportedNodeFormat):
			p.e.Toast(i18n.T("剪贴板中没有可识别的节点信息"), true)
		default:
			p.e.Toast(i18n.Tf("无法解析剪贴板中的节点信息：%s", err.Error()), true)
		}
		return
	}
	p.openManualNode(node)
}

func (p *Page) addNodeMenu(c *ui.Context, m *ui.Menu) {
	if m.Item(i18n.T("导入剪贴板")).Chosen() {
		p.importManualNodeFromClipboard(c)
	}
	m.Separator()
	for _, opt := range protoOptions {
		if m.Item(opt.label).Chosen() {
			p.openAddManualNodeOfType(opt.typ)
		}
	}
}

func (p *Page) openManualNode(n model.ManualNode) {
	if n.Type == "" {
		n.Type = model.ProxyTypeSOCKS5
	}
	opt := protoOptionForType(n.Type)
	port := ""
	if n.Port > 0 {
		port = strconv.Itoa(n.Port)
	} else {
		port = opt.defaultPort
	}
	network := n.Network
	if network == "" {
		network = "tcp"
	}
	hyProto := n.Protocol
	if hyProto == "" {
		hyProto = "udp"
	}
	congestion := n.CongestionController
	if congestion == "" {
		congestion = "bbr"
	}
	udpRelay := n.UDPRelayMode
	if udpRelay == "" {
		udpRelay = "native"
	}
	ssMethod := n.SSOptsMethod
	if ssMethod == "" {
		ssMethod = "aes-128-gcm"
	}
	cipher := n.Cipher
	if cipher == "" {
		switch n.Type {
		case model.ProxyTypeSS:
			cipher = "aes-128-gcm"
		case model.ProxyTypeVMess:
			cipher = "auto"
		}
	}
	idleCheck := ""
	if n.IdleSessionCheckInterval > 0 {
		idleCheck = strconv.Itoa(n.IdleSessionCheckInterval)
	}
	idleTimeout := ""
	if n.IdleSessionTimeout > 0 {
		idleTimeout = strconv.Itoa(n.IdleSessionTimeout)
	}
	minIdle := ""
	if n.MinIdleSession > 0 {
		minIdle = strconv.Itoa(n.MinIdleSession)
	}
	alterID := ""
	if n.AlterID > 0 {
		alterID = strconv.Itoa(n.AlterID)
	}
	advanced := false
	switch opt.typ {
	case model.ProxyTypeSS:
		advanced = n.Plugin != ""
	case model.ProxyTypeVLESS:
		advanced = n.RealityPublicKey != "" || n.RealityShortID != ""
	case model.ProxyTypeHysteria2:
		advanced = n.Up != "" || n.Down != "" || n.Obfs != "" || n.ObfsPassword != "" || len(n.ALPN) > 0
	case model.ProxyTypeAnyTLS:
		advanced = len(n.ALPN) > 0 || n.IdleSessionCheckInterval > 0 || n.IdleSessionTimeout > 0 || n.MinIdleSession > 0
	}
	p.manual = manualNodeDialog{
		open:              true,
		session:           p.manual.session + 1,
		id:                n.ID,
		typ:               opt.typ,
		protoLabel:        opt.label,
		name:              n.Name,
		server:            n.Server,
		port:              port,
		udp:               n.UDP,
		username:          n.Username,
		password:          n.Password,
		uuid:              n.UUID,
		token:             n.Token,
		authStr:           n.AuthStr,
		cipher:            cipher,
		alterID:           alterID,
		flow:              n.Flow,
		packetEncoding:    n.PacketEncoding,
		tls:               n.TLS,
		sni:               n.SNI,
		skipCertVerify:    n.SkipCertVerify,
		clientFingerprint: n.ClientFingerprint,
		alpn:              strings.Join(n.ALPN, ", "),
		realityPublicKey:  n.RealityPublicKey,
		realityShortID:    n.RealityShortID,
		network:           network,
		wsPath:            n.WSPath,
		wsHost:            n.WSHost,
		grpcServiceName:   n.GRPCServiceName,
		plugin:            n.Plugin,
		pluginMode:        n.PluginMode,
		pluginHost:        n.PluginHost,
		ssOptsEnabled:     n.SSOptsEnabled,
		ssOptsMethod:      ssMethod,
		ssOptsPassword:    n.SSOptsPassword,
		hyProtocol:        hyProto,
		ports:             n.Ports,
		up:                n.Up,
		down:              n.Down,
		obfs:              n.Obfs,
		obfsPassword:      n.ObfsPassword,
		congestion:        congestion,
		udpRelayMode:      udpRelay,
		reduceRTT:         n.ReduceRTT,
		disableSNI:        n.DisableSNI,
		idleCheck:         idleCheck,
		idleTimeout:       idleTimeout,
		minIdle:           minIdle,
		advanced:          advanced,
	}
}

func (d *manualNodeDialog) applyProtoSwitch(opt protoOption) {
	prevOpt := protoOptionForType(d.typ)
	d.typ = opt.typ
	if d.port == "" || d.port == prevOpt.defaultPort {
		d.port = opt.defaultPort
	}
	switch opt.typ {
	case model.ProxyTypeSS:
		if d.cipher == "" || d.cipher == "auto" {
			d.cipher = "aes-128-gcm"
		}
		d.udp = true
	case model.ProxyTypeVMess:
		if d.cipher == "" || d.cipher == "aes-128-gcm" {
			d.cipher = "auto"
		}
		d.udp = true
	case model.ProxyTypeVLESS:
		d.tls = true
		d.udp = true
	case model.ProxyTypeTrojan:
		d.udp = true
	case model.ProxyTypeTrojanGo:
		d.udp = true
		d.ssOptsEnabled = true
	case model.ProxyTypeAnyTLS:
		d.udp = true
	}
	d.err = ""
}

func (p *Page) manualDialogView(c *ui.Context) {
	d := &p.manual
	if d.saved {
		d.saved = false
		p.e.Toast(i18n.T("节点已保存"), false)
	}
	if !d.open {
		return
	}
	t := c.Theme()
	title := i18n.T("添加节点")
	if d.id != "" {
		title = i18n.T("编辑节点")
	}

	env.Dialog(c, &d.open, env.DialogOptions{Title: title, Width: 480}, func() {
		ui.Scroll(c.Key(d.session)).MaxHeight(env.DialogMaxHeight(c, 150)).Children(func() {
			ui.Column(c).Gap(10).Padding(0, 4, 4, 0).Children(func() {
				manualField(c, i18n.T("协议"), func() {
					if ui.Select(c.Key("proto"), &d.protoLabel, protoLabels).Label(i18n.T("协议")).Disabled(d.saving).Changed() {
						d.applyProtoSwitch(protoOptionForLabel(d.protoLabel))
					}
				})
				if d.typ == model.ProxyTypeTrojanGo {
					ui.Text(c, i18n.T("Mihomo 通过 trojan 协议 + 传输层 + ss-opts 兼容 Trojan Go；不支持 Trojan Go 原生 mux。")).
						FontSize(11).TextColor(t.TextMuted)
				}
				manualField(c, i18n.T("节点名称"), func() {
					in := ui.TextInput(c.Key("name"), &d.name).Placeholder(i18n.T("例如：香港节点 01")).
						Label(i18n.T("节点名称")).FillWidth().ReadOnly(d.saving)
					if d.id == "" {
						in.AutoFocus()
					}
					if in.Changed() {
						d.err = ""
					}
				})
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.Column(c).Grow(1).Shrink(1).Children(func() {
						manualField(c, i18n.T("服务器"), func() {
							in := ui.TextInput(c.Key("server"), &d.server).Placeholder("example.com").
								Label(i18n.T("服务器")).Font("monospace").FillWidth().ReadOnly(d.saving)
							if in.Changed() {
								d.err = ""
							}
						})
					})
					ui.Column(c).Width(110).Shrink(0).Children(func() {
						manualField(c, i18n.T("节点端口"), func() {
							in := ui.TextInput(c.Key("port"), &d.port).Placeholder("443").
								Label(i18n.T("节点端口")).Font("monospace").FontFeatures("tnum").FillWidth().ReadOnly(d.saving)
							if in.Changed() {
								d.err = ""
							}
						})
					})
				})

				p.manualProtocolFields(c, d)

				if d.err != "" {
					ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
				}
			})
		})
	}, func() {
		if d.id != "" {
			id := d.id
			name := d.name
			if ui.Button(c, i18n.T("删除")).TextColor(t.Danger).Disabled(d.saving).Clicked() {
				p.e.Confirm(i18n.Tf("删除节点「%s」？", name), i18n.T("删除后会重新生成配置。"), i18n.T("删除"), true, func() {
					d.open = false
					p.e.Run(func() error { return p.e.API.DeleteManualNode(id) }, i18n.T("节点已删除"))
				})
			}
		}
		ui.Spacer(c)
		if ui.Button(c, i18n.T("取消")).Disabled(d.saving).Clicked() {
			d.open = false
		}
		label := i18n.T("保存")
		if d.saving {
			label = i18n.T("保存中…")
		}
		if ui.PrimaryButton(c, label).Disabled(d.saving).Clicked() {
			p.saveManualNode()
		}
	})
}

func (p *Page) manualProtocolFields(c *ui.Context, d *manualNodeDialog) {
	switch d.typ {
	case model.ProxyTypeSOCKS5:
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("用户名（可选）"), func() {
					ui.TextInput(c.Key("user"), &d.username).Label(i18n.T("用户名")).FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("密码（可选）"), func() {
					ui.TextInput(c.Key("pass"), &d.password).Label(i18n.T("密码")).Password().FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &d.udp, i18n.T("启用 UDP")).Disabled(d.saving)
			ui.Checkbox(c, &d.tls, i18n.T("启用 TLS")).Disabled(d.saving)
			if d.tls {
				ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
			}
		})

	case model.ProxyTypeHTTP:
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("用户名（可选）"), func() {
					ui.TextInput(c.Key("user"), &d.username).Label(i18n.T("用户名")).FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("密码（可选）"), func() {
					ui.TextInput(c.Key("pass"), &d.password).Label(i18n.T("密码")).Password().FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &d.tls, i18n.T("HTTPS（TLS）")).Disabled(d.saving)
			if d.tls {
				ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
			}
		})
		if d.tls {
			manualField(c, "SNI", func() {
				ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
			})
		}

	case model.ProxyTypeSS:
		manualField(c, i18n.T("加密方式（Cipher）"), func() {
			ui.TextInput(c.Key("cipher"), &d.cipher).Placeholder("aes-128-gcm / chacha20-ietf-poly1305").
				Label(i18n.T("加密方式")).Font("monospace").FillWidth().ReadOnly(d.saving)
		})
		manualField(c, i18n.T("密码"), func() {
			ui.TextInput(c.Key("pass"), &d.password).Label(i18n.T("密码")).Password().FillWidth().ReadOnly(d.saving)
		})
		ui.Checkbox(c, &d.udp, i18n.T("启用 UDP")).Disabled(d.saving)
		ui.Collapsible(c, i18n.T("高级：插件（obfs / v2ray-plugin）"), &d.advanced, func() {
			ui.Column(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
				manualField(c, i18n.T("插件名称（可选）"), func() {
					ui.TextInput(c.Key("plugin"), &d.plugin).Placeholder("obfs").Label(i18n.T("插件名称")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
				if strings.TrimSpace(d.plugin) != "" {
					ui.Row(c).Gap(8).FillWidth().Children(func() {
						ui.Column(c).Grow(1).Children(func() {
							manualField(c, i18n.T("插件模式"), func() {
								ui.TextInput(c.Key("pmode"), &d.pluginMode).Placeholder("tls / websocket").Label(i18n.T("插件模式")).Font("monospace").FillWidth().ReadOnly(d.saving)
							})
						})
						ui.Column(c).Grow(1).Children(func() {
							manualField(c, i18n.T("插件 Host"), func() {
								ui.TextInput(c.Key("phost"), &d.pluginHost).Placeholder("example.com").Label(i18n.T("插件 Host")).Font("monospace").FillWidth().ReadOnly(d.saving)
							})
						})
					})
				}
			})
		})

	case model.ProxyTypeVMess:
		manualField(c, "UUID", func() {
			ui.TextInput(c.Key("uuid"), &d.uuid).Placeholder("00000000-0000-0000-0000-000000000000").
				Label("UUID").Font("monospace").FillWidth().ReadOnly(d.saving)
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("加密方式（Cipher）"), func() {
					ui.TextInput(c.Key("cipher"), &d.cipher).Placeholder("auto").Label(i18n.T("加密方式")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Width(100).Shrink(0).Children(func() {
				manualField(c, "AlterID", func() {
					ui.TextInput(c.Key("aid"), &d.alterID).Placeholder("0").Label("AlterID").Font("monospace").FontFeatures("tnum").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &d.udp, i18n.T("启用 UDP")).Disabled(d.saving)
			ui.Checkbox(c, &d.tls, i18n.T("启用 TLS")).Disabled(d.saving)
			if d.tls {
				ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
			}
		})
		if d.tls {
			ui.Row(c).Gap(8).FillWidth().Children(func() {
				ui.Column(c).Grow(1).Children(func() {
					manualField(c, "SNI / ServerName", func() {
						ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
					})
				})
				ui.Column(c).Grow(1).Children(func() {
					manualField(c, i18n.T("客户端指纹（可选）"), func() {
						ui.TextInput(c.Key("fp"), &d.clientFingerprint).Placeholder("chrome").Label(i18n.T("客户端指纹")).Font("monospace").FillWidth().ReadOnly(d.saving)
					})
				})
			})
		}
		manualTransportFields(c, d)

	case model.ProxyTypeVLESS:
		manualField(c, "UUID", func() {
			ui.TextInput(c.Key("uuid"), &d.uuid).Placeholder("00000000-0000-0000-0000-000000000000").
				Label("UUID").Font("monospace").FillWidth().ReadOnly(d.saving)
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("流控（Flow，可选）"), func() {
					ui.TextInput(c.Key("flow"), &d.flow).Placeholder("xtls-rprx-vision").Label("Flow").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("数据包编码（可选）"), func() {
					ui.TextInput(c.Key("pkt"), &d.packetEncoding).Placeholder("xudp").Label(i18n.T("数据包编码")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &d.udp, i18n.T("启用 UDP")).Disabled(d.saving)
			ui.Checkbox(c, &d.tls, i18n.T("启用 TLS")).Disabled(d.saving)
			ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, "SNI / ServerName", func() {
					ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("客户端指纹（可选）"), func() {
					ui.TextInput(c.Key("fp"), &d.clientFingerprint).Placeholder("chrome").Label(i18n.T("客户端指纹")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		manualTransportFields(c, d)
		ui.Collapsible(c, i18n.T("高级：Reality"), &d.advanced, func() {
			ui.Row(c).Gap(8).Padding(4, 0, 0, 0).FillWidth().Children(func() {
				ui.Column(c).Grow(1).Children(func() {
					manualField(c, "Public Key", func() {
						ui.TextInput(c.Key("rpub"), &d.realityPublicKey).Label("Public Key").Font("monospace").FillWidth().ReadOnly(d.saving)
					})
				})
				ui.Column(c).Width(140).Shrink(0).Children(func() {
					manualField(c, "Short ID", func() {
						ui.TextInput(c.Key("rsid"), &d.realityShortID).Label("Short ID").Font("monospace").FillWidth().ReadOnly(d.saving)
					})
				})
			})
		})

	case model.ProxyTypeTrojan, model.ProxyTypeTrojanGo:
		manualField(c, i18n.T("密码"), func() {
			ui.TextInput(c.Key("pass"), &d.password).Label(i18n.T("密码")).Password().FillWidth().ReadOnly(d.saving)
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &d.udp, i18n.T("启用 UDP")).Disabled(d.saving)
			ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, "SNI", func() {
					ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("客户端指纹（可选）"), func() {
					ui.TextInput(c.Key("fp"), &d.clientFingerprint).Placeholder("chrome").Label(i18n.T("客户端指纹")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		manualField(c, i18n.T("ALPN（逗号分隔，可选）"), func() {
			ui.TextInput(c.Key("alpn"), &d.alpn).Placeholder("h2, http/1.1").Label("ALPN").Font("monospace").FillWidth().ReadOnly(d.saving)
		})
		manualTransportFields(c, d)
		if d.typ == model.ProxyTypeTrojanGo {
			ui.Checkbox(c, &d.ssOptsEnabled, i18n.T("启用 Shadowsocks 二次加密（ss-opts）")).Disabled(d.saving)
			if d.ssOptsEnabled {
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("SS 加密方式"), func() {
							ui.Select(c.Key("ssmethod"), &d.ssOptsMethod, ssOptsMethodChoice).Label(i18n.T("SS 加密方式")).Disabled(d.saving)
						})
					})
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("SS 密码"), func() {
							ui.TextInput(c.Key("sspass"), &d.ssOptsPassword).Label(i18n.T("SS 密码")).Password().FillWidth().ReadOnly(d.saving)
						})
					})
				})
			}
		}

	case model.ProxyTypeHysteria:
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("上行带宽（必填）"), func() {
					ui.TextInput(c.Key("up"), &d.up).Placeholder("50 Mbps").Label(i18n.T("上行带宽")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("下行带宽（必填）"), func() {
					ui.TextInput(c.Key("down"), &d.down).Placeholder("200 Mbps").Label(i18n.T("下行带宽")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("认证密钥（auth-str）"), func() {
					ui.TextInput(c.Key("auth"), &d.authStr).Label(i18n.T("认证密钥")).Password().FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Width(150).Shrink(0).Children(func() {
				manualField(c, i18n.T("传输协议"), func() {
					ui.Select(c.Key("hyproto"), &d.hyProtocol, hysteriaProtoOpts).Label(i18n.T("传输协议")).Disabled(d.saving)
				})
			})
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, "SNI", func() {
					ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("混淆密钥（obfs，可选）"), func() {
					ui.TextInput(c.Key("obfs"), &d.obfs).Label(i18n.T("混淆密钥")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)

	case model.ProxyTypeHysteria2:
		manualField(c, i18n.T("密码"), func() {
			ui.TextInput(c.Key("pass"), &d.password).Label(i18n.T("密码")).Password().FillWidth().ReadOnly(d.saving)
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, "SNI", func() {
					ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Width(150).Shrink(0).Children(func() {
				manualField(c, i18n.T("跳跃端口（可选）"), func() {
					ui.TextInput(c.Key("ports"), &d.ports).Placeholder("443-8443").Label(i18n.T("跳跃端口")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
		ui.Collapsible(c, i18n.T("高级：带宽限制、混淆（obfs）、ALPN"), &d.advanced, func() {
			ui.Column(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("上行带宽（可选）"), func() {
							ui.TextInput(c.Key("up"), &d.up).Placeholder("50 Mbps").Label(i18n.T("上行带宽")).Font("monospace").FillWidth().ReadOnly(d.saving)
						})
					})
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("下行带宽（可选）"), func() {
							ui.TextInput(c.Key("down"), &d.down).Placeholder("200 Mbps").Label(i18n.T("下行带宽")).Font("monospace").FillWidth().ReadOnly(d.saving)
						})
					})
				})
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("混淆类型（可选）"), func() {
							ui.TextInput(c.Key("obfs"), &d.obfs).Placeholder("salamander").Label(i18n.T("混淆类型")).Font("monospace").FillWidth().ReadOnly(d.saving)
						})
					})
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("混淆密码（可选）"), func() {
							ui.TextInput(c.Key("obfspw"), &d.obfsPassword).Label(i18n.T("混淆密码")).Password().FillWidth().ReadOnly(d.saving)
						})
					})
				})
				manualField(c, i18n.T("ALPN（逗号分隔，可选）"), func() {
					ui.TextInput(c.Key("alpn"), &d.alpn).Placeholder("h3").Label("ALPN").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})

	case model.ProxyTypeTUIC:
		manualField(c, i18n.T("UUID（TUIC v5）"), func() {
			ui.TextInput(c.Key("uuid"), &d.uuid).Placeholder("00000000-0000-0000-0000-000000000000").
				Label("UUID").Font("monospace").FillWidth().ReadOnly(d.saving)
		})
		manualField(c, i18n.T("密码（TUIC v5）"), func() {
			ui.TextInput(c.Key("pass"), &d.password).Label(i18n.T("密码")).Password().FillWidth().ReadOnly(d.saving)
		})
		manualField(c, i18n.T("Token（仅 TUIC v4，可选）"), func() {
			ui.TextInput(c.Key("token"), &d.token).Label("Token").Password().FillWidth().ReadOnly(d.saving)
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("拥塞控制"), func() {
					ui.Select(c.Key("cc"), &d.congestion, congestionOptions).Label(i18n.T("拥塞控制")).Disabled(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("UDP 转发模式"), func() {
					ui.Select(c.Key("udpmode"), &d.udpRelayMode, udpRelayOptions).Label(i18n.T("UDP 转发模式")).Disabled(d.saving)
				})
			})
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, "SNI", func() {
					ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("ALPN（逗号分隔，可选）"), func() {
					ui.TextInput(c.Key("alpn"), &d.alpn).Placeholder("h3").Label("ALPN").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
			ui.Checkbox(c, &d.reduceRTT, "Reduce RTT").Disabled(d.saving)
			ui.Checkbox(c, &d.disableSNI, i18n.T("禁用 SNI")).Disabled(d.saving)
		})

	case model.ProxyTypeAnyTLS:
		manualField(c, i18n.T("密码"), func() {
			ui.TextInput(c.Key("pass"), &d.password).Label(i18n.T("密码")).Password().FillWidth().ReadOnly(d.saving)
		})
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, "SNI", func() {
					ui.TextInput(c.Key("sni"), &d.sni).Placeholder("example.com").Label("SNI").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("客户端指纹（可选）"), func() {
					ui.TextInput(c.Key("fp"), &d.clientFingerprint).Placeholder("chrome").Label(i18n.T("客户端指纹")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &d.udp, i18n.T("启用 UDP")).Disabled(d.saving)
			ui.Checkbox(c, &d.skipCertVerify, i18n.T("跳过证书验证")).Disabled(d.saving)
		})
		ui.Collapsible(c, i18n.T("高级：ALPN、会话保活"), &d.advanced, func() {
			ui.Column(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
				manualField(c, i18n.T("ALPN（逗号分隔，可选）"), func() {
					ui.TextInput(c.Key("alpn"), &d.alpn).Placeholder("h2, http/1.1").Label("ALPN").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("检测间隔（秒）"), func() {
							ui.TextInput(c.Key("ichk"), &d.idleCheck).Placeholder("30").Label(i18n.T("检测间隔")).Font("monospace").FontFeatures("tnum").FillWidth().ReadOnly(d.saving)
						})
					})
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("空闲超时（秒）"), func() {
							ui.TextInput(c.Key("ito"), &d.idleTimeout).Placeholder("30").Label(i18n.T("空闲超时")).Font("monospace").FontFeatures("tnum").FillWidth().ReadOnly(d.saving)
						})
					})
					ui.Column(c).Grow(1).Children(func() {
						manualField(c, i18n.T("最小空闲会话"), func() {
							ui.TextInput(c.Key("imin"), &d.minIdle).Placeholder("0").Label(i18n.T("最小空闲会话")).Font("monospace").FontFeatures("tnum").FillWidth().ReadOnly(d.saving)
						})
					})
				})
			})
		})
	}
}

func manualTransportFields(c *ui.Context, d *manualNodeDialog) {
	manualField(c, i18n.T("传输方式（Network）"), func() {
		ui.Select(c.Key("net"), &d.network, networkOptions).Label(i18n.T("传输方式")).Disabled(d.saving)
	})
	switch d.network {
	case "ws":
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, i18n.T("WS 路径（可选）"), func() {
					ui.TextInput(c.Key("wspath"), &d.wsPath).Placeholder("/ws").Label(i18n.T("WS 路径")).Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
			ui.Column(c).Grow(1).Children(func() {
				manualField(c, "WS Host", func() {
					ui.TextInput(c.Key("wshost"), &d.wsHost).Placeholder("example.com").Label("WS Host").Font("monospace").FillWidth().ReadOnly(d.saving)
				})
			})
		})
	case "grpc":
		manualField(c, "gRPC Service Name", func() {
			ui.TextInput(c.Key("grpc"), &d.grpcServiceName).Placeholder("GunService").Label("gRPC Service Name").Font("monospace").FillWidth().ReadOnly(d.saving)
		})
	}
}

func manualField(c *ui.Context, label string, control func()) {
	t := c.Theme()
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
		control()
	})
}

func (d *manualNodeDialog) toModel() (model.ManualNode, error) {
	port, err := strconv.Atoi(strings.TrimSpace(d.port))
	if err != nil {
		return model.ManualNode{}, err
	}
	var alpn []string
	for _, part := range strings.Split(d.alpn, ",") {
		if s := strings.TrimSpace(part); s != "" {
			alpn = append(alpn, s)
		}
	}
	aid, _ := strconv.Atoi(strings.TrimSpace(d.alterID))
	ic, _ := strconv.Atoi(strings.TrimSpace(d.idleCheck))
	it, _ := strconv.Atoi(strings.TrimSpace(d.idleTimeout))
	im, _ := strconv.Atoi(strings.TrimSpace(d.minIdle))

	netw := d.network
	if d.typ != model.ProxyTypeVMess && d.typ != model.ProxyTypeVLESS &&
		d.typ != model.ProxyTypeTrojan && d.typ != model.ProxyTypeTrojanGo {
		netw = ""
	}
	return model.ManualNode{
		ID:                       d.id,
		Name:                     d.name,
		Type:                     d.typ,
		Server:                   d.server,
		Port:                     port,
		UDP:                      d.udp,
		Username:                 d.username,
		Password:                 d.password,
		UUID:                     d.uuid,
		Token:                    d.token,
		AuthStr:                  d.authStr,
		Cipher:                   d.cipher,
		AlterID:                  aid,
		Flow:                     d.flow,
		PacketEncoding:           d.packetEncoding,
		TLS:                      d.tls,
		SNI:                      d.sni,
		SkipCertVerify:           d.skipCertVerify,
		ClientFingerprint:        d.clientFingerprint,
		ALPN:                     alpn,
		RealityPublicKey:         d.realityPublicKey,
		RealityShortID:           d.realityShortID,
		Network:                  netw,
		WSPath:                   d.wsPath,
		WSHost:                   d.wsHost,
		GRPCServiceName:          d.grpcServiceName,
		Plugin:                   d.plugin,
		PluginMode:               d.pluginMode,
		PluginHost:               d.pluginHost,
		SSOptsEnabled:            d.ssOptsEnabled,
		SSOptsMethod:             d.ssOptsMethod,
		SSOptsPassword:           d.ssOptsPassword,
		Protocol:                 d.hyProtocol,
		Ports:                    d.ports,
		Up:                       d.up,
		Down:                     d.down,
		Obfs:                     d.obfs,
		ObfsPassword:             d.obfsPassword,
		CongestionController:     d.congestion,
		UDPRelayMode:             d.udpRelayMode,
		ReduceRTT:                d.reduceRTT,
		DisableSNI:               d.disableSNI,
		IdleSessionCheckInterval: ic,
		IdleSessionTimeout:       it,
		MinIdleSession:           im,
	}, nil
}

func (p *Page) saveManualNode() {
	d := &p.manual
	if d.saving {
		return
	}
	n, err := d.toModel()
	if err != nil {
		d.err = i18n.T("端口须为 1–65535 之间的数字。")
		return
	}
	d.err = ""
	d.saving = true
	p.e.Run(func() error {
		_, err := p.e.API.SaveManualNode(n)
		if err != nil {
			d.saving = false
			d.err = err.Error()
			return nil
		}
		d.saving = false
		d.open = false
		d.saved = true
		return nil
	}, "")
}
