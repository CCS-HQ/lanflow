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

	// Open the Add Node dialog from header button.
	if err := tt.Click("添加节点"); err != nil {
		t.Fatalf("click 添加节点: %v", err)
	}
	tt.Frame()
	if !p.manual.open {
		t.Fatal("expected manual node dialog to open")
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
