package view

import (
	"bytes"
	"image"
	"image/color"
	_ "image/jpeg" // group icons may be JPEG
	"image/png"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/view/env"
)

// Terminal proxy commands for the tray's 复制终端代理命令 submenu, one per
// shell family (docs/design.md §7); the overview shows one of the same
// (env.ShellCommandFor).

// shellCommand is one variant of the terminal proxy command.
type shellCommand = env.ShellCommand

// shellCommands returns the variants for port: bash/zsh, fish, PowerShell
// and cmd.
func shellCommands(port int) []shellCommand { return env.ShellCommands(port) }

// menuIconPNG renders icon bytes (SVG, PNG or JPEG) as a small PNG for a
// menu item; nil when the data cannot be drawn.
func menuIconPNG(data []byte, size int, dark bool) []byte {
	trimmed := bytes.TrimSpace(data)
	var img image.Image
	if bytes.HasPrefix(trimmed, []byte("<svg")) || bytes.HasPrefix(trimmed, []byte("<?xml")) {
		s, err := ui.ParseSVG(trimmed)
		if err != nil {
			return nil
		}
		fg := color.NRGBA{0, 0, 0, 255}
		if dark && runtime.GOOS == "windows" {
			fg = color.NRGBA{255, 255, 255, 255}
		}
		img = s.Image(size, size, fg)
	} else {
		decoded, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		img = decoded
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// menuIcon returns the menu icon of a group icon reference, cached by
// reference; nil while the data is not loaded or cannot be drawn.
func (tr *tray) menuIcon(ref string) *mygo.MenuIcon {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	tr.mu.Lock()
	if ic, ok := tr.icons[ref]; ok {
		tr.mu.Unlock()
		return ic
	}
	tr.mu.Unlock()
	data, ok := tr.h.hostAPI().IconData(ref)
	if !ok {
		return nil
	}
	var ic *mygo.MenuIcon
	dark := tr.t != nil && tr.t.Dark()
	if pngData := menuIconPNG(data, 32, dark); pngData != nil {
		ic, _ = mygo.NewMenuIcon(pngData, false)
	}
	tr.mu.Lock()
	if tr.icons == nil {
		tr.icons = map[string]*mygo.MenuIcon{}
	}
	tr.icons[ref] = ic // nil too: never retried
	tr.mu.Unlock()
	return ic
}
