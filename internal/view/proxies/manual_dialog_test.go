package proxies

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

func TestManualNodeDialogAllProtocolsAndCRUD(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 720)

	// Open the Add Node dropdown menu from header button.
	if err := tt.Click("添加节点"); err != nil {
		t.Fatalf("click 添加节点: %v", err)
	}
	menu := tt.Menu()
	if len(menu) < 1+len(protoOptions) || menu[0] != "导入剪贴板" {
		t.Fatalf("unexpected menu items: %q", menu)
	}
	if err := tt.ChooseMenuItem("Shadowsocks"); err != nil {
		t.Fatalf("choose Shadowsocks: %v (menu %q)", err, menu)
	}
	tt.Frame()
	if !p.manual.open || p.manual.typ != model.ProxyTypeSS || p.manual.port != "8388" {
		t.Fatalf("expected manual node dialog for SS, got open=%v typ=%q port=%q", p.manual.open, p.manual.typ, p.manual.port)
	}

	// Verify all 11 supported protocols are selectable and switch cleanly.
	for _, opt := range protoOptions {
		p.manual.applyProtoSwitch(opt)
		p.manual.protoLabel = opt.label
		tt.Frame()
		if !tt.HasText(opt.label) {
			t.Fatalf("expected protocol label %q in dialog", opt.label)
		}
	}

	// Fill and save a Shadowsocks node.
	p.manual.applyProtoSwitch(protoOptionForType(model.ProxyTypeSS))
	p.manual.protoLabel = "Shadowsocks"
	p.manual.name = "manual-ss-hk"
	p.manual.server = "1.2.3.4"
	p.manual.port = "8388"
	p.manual.cipher = "aes-128-gcm"
	p.manual.password = "secret"
	p.saveManualNode()
	tt.Frame()

	if p.manual.open {
		t.Fatalf("expected dialog to close after save, err=%q", p.manual.err)
	}
	nodes := api.ManualNodes()
	if len(nodes) != 1 || nodes[0].Name != "manual-ss-hk" {
		t.Fatalf("ManualNodes = %+v", nodes)
	}
	if len(*toasts) == 0 {
		t.Fatal("expected save toast")
	}

	// Open edit dialog and update port.
	p.openManualNode(nodes[0])
	tt.Frame()
	p.manual.port = "9388"
	p.saveManualNode()
	tt.Frame()
	if got := api.ManualNodes(); len(got) != 1 || got[0].Port != 9388 {
		t.Fatalf("edited ManualNodes = %+v", got)
	}

	// Delete node.
	p.askDeleteManualNode(api.ManualNodes()[0])
	tt.Frame()
	if got := api.ManualNodes(); len(got) != 0 {
		t.Fatalf("expected 0 nodes after delete, got %+v", got)
	}
}

func TestManualNodeImportFromClipboard(t *testing.T) {
	api := fake.NewInstant()
	e, toasts := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 900, 720)

	// 1. Empty clipboard shows error toast and keeps dialog closed.
	tt.SetClipboard("   ")
	if err := tt.Click("添加节点"); err != nil {
		t.Fatalf("click 添加节点: %v", err)
	}
	if err := tt.ChooseMenuItem("导入剪贴板"); err != nil {
		t.Fatalf("choose 导入剪贴板: %v", err)
	}
	tt.Frame()
	if p.manual.open {
		t.Fatal("expected dialog to remain closed for empty clipboard")
	}
	if len(*toasts) == 0 || (*toasts)[len(*toasts)-1] != "剪贴板为空" {
		t.Fatalf("expected empty clipboard toast, got %v", *toasts)
	}

	// 2. Unrecognized text shows unsupported format toast.
	tt.SetClipboard("not a proxy link")
	if err := tt.Click("添加节点"); err != nil {
		t.Fatalf("click 添加节点: %v", err)
	}
	if err := tt.ChooseMenuItem("导入剪贴板"); err != nil {
		t.Fatalf("choose 导入剪贴板: %v", err)
	}
	tt.Frame()
	if p.manual.open {
		t.Fatal("expected dialog to remain closed for invalid clipboard")
	}
	if (*toasts)[len(*toasts)-1] != "剪贴板中没有可识别的节点信息" {
		t.Fatalf("expected unsupported format toast, got %v", *toasts)
	}

	// 3. Valid VLESS Reality URI opens edit modal prefilled, allows edit, and saves.
	tt.SetClipboard("vless://22222222-2222-2222-2222-222222222222@vless.example.com:443?flow=xtls-rprx-vision&security=reality&sni=cdn.example.com&fp=chrome&pbk=pubkey123&sid=abcd#Clipboard%20VLESS")
	if err := tt.Click("添加节点"); err != nil {
		t.Fatalf("click 添加节点: %v", err)
	}
	if err := tt.ChooseMenuItem("导入剪贴板"); err != nil {
		t.Fatalf("choose 导入剪贴板: %v", err)
	}
	tt.Frame()
	if !p.manual.open {
		t.Fatal("expected edit modal to open after clipboard import")
	}
	if p.manual.typ != model.ProxyTypeVLESS || p.manual.name != "Clipboard VLESS" ||
		p.manual.server != "vless.example.com" || p.manual.port != "443" ||
		p.manual.uuid != "22222222-2222-2222-2222-222222222222" ||
		p.manual.flow != "xtls-rprx-vision" || p.manual.sni != "cdn.example.com" ||
		p.manual.realityPublicKey != "pubkey123" || p.manual.realityShortID != "abcd" ||
		!p.manual.advanced {
		t.Fatalf("unexpected prefilled dialog state: %+v", p.manual)
	}
	p.manual.name = "Clipboard VLESS Edited"
	p.saveManualNode()
	tt.Frame()
	if p.manual.open {
		t.Fatalf("expected dialog to close after save, err=%q", p.manual.err)
	}
	nodes := api.ManualNodes()
	if len(nodes) != 1 || nodes[0].Name != "Clipboard VLESS Edited" || nodes[0].RealityPublicKey != "pubkey123" {
		t.Fatalf("ManualNodes = %+v", nodes)
	}

	// 4. Clash YAML snippet opens edit modal prefilled and saves cleanly.
	tt.SetClipboard("- name: yaml-ss\n  type: ss\n  server: ss.example.com\n  port: 8388\n  cipher: aes-128-gcm\n  password: pw")
	if err := tt.Click("添加节点"); err != nil {
		t.Fatalf("click 添加节点: %v", err)
	}
	if err := tt.ChooseMenuItem("导入剪贴板"); err != nil {
		t.Fatalf("choose 导入剪贴板: %v", err)
	}
	tt.Frame()
	if !p.manual.open || p.manual.typ != model.ProxyTypeSS || p.manual.name != "yaml-ss" || p.manual.server != "ss.example.com" {
		t.Fatalf("unexpected YAML prefilled state: %+v", p.manual)
	}
	p.saveManualNode()
	tt.Frame()
	if p.manual.open || len(api.ManualNodes()) != 2 {
		t.Fatalf("expected 2 saved nodes, open=%v err=%q nodes=%+v", p.manual.open, p.manual.err, api.ManualNodes())
	}
}
