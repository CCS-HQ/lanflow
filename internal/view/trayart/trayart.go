// Package trayart draws the menu bar / notification area icon and
// composes the text beside it (docs/design.md §7), for the tray and the
// settings preview.
package trayart

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// State is one of the four icon states (docs/design.md §7).
type State int

const (
	Off   State = iota // core stopped: faint
	On                 // running, system proxy on: solid
	Half               // running, system proxy off: translucent
	Error              // error: with a red badge
)

// The menu bar / notification area icon (docs/design.md §7): the Lanflow
// mark, three waves, drawn at one weight in every state. The state shows
// in which waves are solid: all three when traffic is taken over (system
// proxy or TUN), the middle one when only the core runs, none when it is
// stopped. A dot in the top right corner marks HTTPS decryption, a red one
// an error. On macOS a small traffic graph may follow the mark.

// Look is everything the icon shows.
type Look struct {
	State      State
	TUN        bool      // TUN mode active: drawn in a distinct colour
	Capture    bool      // HTTPS decryption on
	Graph      []float64 // nil = no graph; else bar heights 0..1, oldest first
	SpeedLines [2]string // optional 2-line stacked speed (macOS compact bar: [0]=up, [1]=down)
}

func (l Look) hasSpeed() bool {
	return l.SpeedLines[0] != "" || l.SpeedLines[1] != ""
}

func (l Look) Key() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%v|%v|%v|%s|%s|", l.State, l.TUN, l.Capture, l.Graph != nil, l.SpeedLines[0], l.SpeedLines[1])
	for _, v := range l.Graph {
		fmt.Fprintf(&b, "%.2f,", v)
	}
	return b.String()
}

// Ink is the icon's colour scheme (docs/design.md §3.7, §7): macOS
// uses template icons (black with alpha, the bar tints them), except for
// the error badge and TUN's distinct colour; Windows draws in colour: the
// system accent when running, emerald green when TUN is active, grey when
// stopped.
type Ink struct {
	Windows bool
	Dark    bool        // the bar is dark (error/TUN icon on macOS, grey/TUN on Windows)
	Accent  color.NRGBA // Windows accent; zero = default blue
}

const (
	waveWidth = "1.4"
	faint     = 0.32 // opacity of an inactive wave
	GraphBars = 8
	// graphLevels is how many heights above zero a bar of the graph takes.
	graphLevels = 10
	graphW      = 18.0 // the graph's width in points, after a 2.5-point gap
	// speedW is the 2-line stacked speed block width in points (at 2× = 120px;
	// 36px icon + 120px speed = 156×36px, matching Clash Party's macOS tray
	// canvas with generous breathing room between the icon and 0.00 B/s).
	speedW = 60.0
)

func iconWidth(l Look) float64 {
	w := 18.0
	if l.Graph != nil {
		w += 3 + graphW
	}
	return w
}

// The waves, inset to a ~14pt visual footprint inside the 18pt macOS status
// item frame (matching Clash Party's 16pt tray icon).
const (
	waveTop    = "M3.4 6c1.9-1.5 3.7-1.5 5.6 0s3.7 1.5 5.6 0"
	waveTopCut = "M3.4 6c1.9-1.5 3.7-1.5 5.6 0"
	waveMid    = "M3.4 9.2c1.9-1.5 3.7-1.5 5.6 0s3.7 1.5 5.6 0"
	waveBottom = "M3.4 12.4c1.9-1.5 3.7-1.5 5.6 0s3.7 1.5 5.6 0"
)

func waveOpacity(state State) [3]float64 {
	switch state {
	case On:
		return [3]float64{1, 1, 1}
	case Half:
		return [3]float64{faint, 1, faint}
	default: // off, error
		return [3]float64{faint, faint, faint}
	}
}

// SVG is the icon (waves and optional traffic graph) as SVG in points (18 tall).
func SVG(l Look) string {
	w := iconWidth(l)
	op := waveOpacity(l.State)
	badge := l.Capture || l.State == Error
	top := waveTop
	if badge {
		top = waveTopCut
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %g 18" fill="none" stroke="currentColor" stroke-width="%s" stroke-linecap="round">`, w, waveWidth)
	for i, d := range []string{top, waveMid, waveBottom} {
		fmt.Fprintf(&b, `<path d="%s" stroke-opacity="%.2f"/>`, d, op[i])
	}
	switch {
	case l.State == Error:
		b.WriteString(`<circle cx="14" cy="4.6" r="2.4" fill="#FF3B30" stroke="none"/>`)
	case l.Capture:
		b.WriteString(`<circle cx="14" cy="4.6" r="2.1" fill="currentColor" stroke="none"/>`)
	}
	if l.Graph != nil {
		// bars on a faint baseline, bottom-aligned with the lowest wave
		x0, base, h := 21.0, 15.5, 12.0
		step := graphW / GraphBars
		bw := step * 0.62
		fmt.Fprintf(&b, `<line x1="%g" y1="%g" x2="%g" y2="%g" stroke-width="1" stroke-opacity="0.3"/>`, x0, base+0.6, x0+graphW-(step-bw), base+0.6)
		alpha := 1.0
		if l.State != On && l.State != Half {
			alpha = faint
		}
		for i, v := range l.Graph {
			if v <= 0 {
				continue
			}
			bh := math.Max(1, math.Min(1, v)*h)
			fmt.Fprintf(&b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="0.5" fill="currentColor" fill-opacity="%.2f" stroke="none"/>`,
				x0+float64(i)*step, base-bh, bw, bh, alpha)
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

var (
	speedFaceOnce sync.Once
	speedFaceMu   sync.Mutex
	speedFace     font.Face
)

func loadTTCFont(path, fullName string) *sfnt.Font {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	col, err := opentype.ParseCollection(raw)
	if err != nil {
		return nil
	}
	var buf sfnt.Buffer
	for i := 0; i < col.NumFonts(); i++ {
		f, err := col.Font(i)
		if err != nil {
			continue
		}
		if name, err := f.Name(&buf, sfnt.NameIDFull); err == nil && name == fullName {
			return f
		}
	}
	return nil
}

func getSpeedFace() font.Face {
	speedFaceOnce.Do(func() {
		// Prefer macOS Helvetica Neue Medium (crisp medium weight matching
		// Clash Party's 2-line menu bar indicator), falling back to Arial Bold
		// and embedded Go Bold on non-macOS systems.
		ft := loadTTCFont("/System/Library/Fonts/HelveticaNeue.ttc", "Helvetica Neue Medium")
		if ft == nil {
			for _, p := range []string{
				"/System/Library/Fonts/Supplemental/Arial Bold.ttf",
				"/System/Library/Fonts/SFNS.ttf",
			} {
				if b, err := os.ReadFile(p); err == nil {
					if parsed, err := opentype.Parse(b); err == nil {
						ft = parsed
						break
					}
				}
			}
		}
		if ft == nil {
			ft, _ = opentype.Parse(gobold.TTF)
		}
		if ft != nil {
			// Render at 2× supersampling (Size 36 on a 72px buffer, then 2×2
			// box-downsampled to 36px) with HintingNone for smooth macOS-grade
			// anti-aliasing matching Clash Party's Canvas rendering.
			face, err := opentype.NewFace(ft, &opentype.FaceOptions{
				Size:    36,
				DPI:     72,
				Hinting: font.HintingNone,
			})
			if err == nil {
				speedFace = face
			}
		}
	})
	return speedFace
}

// CompactRate formats a rate for the 2-line stacked menu bar icon, matching
// Clash Party's calcTraffic format: "0.00 B/s", "512.0 B/s", "9.75 KB/s",
// "12.34 KB/s", "100.5 KB/s", "1.32 MB/s".
func CompactRate(b int64) string {
	if b < 0 {
		b = 0
	}
	v := float64(b)
	switch {
	case b < 1024:
		return formatTrayNum(v) + " B/s"
	case b < 1<<20:
		return formatTrayNum(v/1024) + " KB/s"
	case b < 1<<30:
		return formatTrayNum(v/(1<<20)) + " MB/s"
	default:
		return formatTrayNum(v/(1<<30)) + " GB/s"
	}
}

func formatTrayNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	if len(s) <= 5 {
		return s
	}
	if len(s) == 6 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", math.Round(v))
}

// tunColor returns the distinct icon colour used when TUN mode is active,
// tuned for contrast against light and dark taskbars / menu bars.
func tunColor(dark bool) color.NRGBA {
	if dark {
		return color.NRGBA{52, 211, 126, 255} // bright emerald on dark bar
	}
	return color.NRGBA{16, 158, 86, 255} // emerald green on light bar
}

// PNG renders the icon at 2× (36 pixels tall), optionally followed by two
// stacked lines of native-font speed text (matching Clash Party on macOS).
func PNG(l Look, ink Ink) []byte {
	var col color.NRGBA
	if l.TUN && l.State == On {
		col = tunColor(ink.Dark)
	} else if ink.Windows {
		accent := ink.Accent
		if accent.A == 0 {
			accent = color.NRGBA{0, 120, 212, 255} // Windows default blue
		}
		grey := color.NRGBA{110, 110, 110, 255}
		if ink.Dark {
			grey = color.NRGBA{160, 160, 160, 255}
		}
		col = grey
		if l.State == On || l.State == Half {
			col = accent
		}
	} else {
		col = color.NRGBA{0, 0, 0, 255}
		if ink.Dark {
			col = color.NRGBA{255, 255, 255, 255}
		}
	}
	s := ui.MustParseSVG([]byte(SVG(l)))
	iconPx := int(math.Round(iconWidth(l) * 2))
	img := s.Image(iconPx, 36, col)

	var out image.Image = img
	if l.hasSpeed() {
		speedPx := int(math.Round(speedW * 2))
		totalW := iconPx + speedPx
		dst := image.NewNRGBA(image.Rect(0, 0, totalW, 36))
		draw.Draw(dst, img.Bounds(), img, image.Point{}, draw.Src)
		if face := getSpeedFace(); face != nil {
			// Draw text into a 2× supersampled buffer (speedPx*2 × 72) and
			// box-downsample 2×2 into dst for crisp, even anti-aliased stems.
			hiW, hiH := speedPx*2, 72
			hi := image.NewAlpha(image.Rect(0, 0, hiW, hiH))
			speedFaceMu.Lock()
			d := &font.Drawer{
				Dst:  hi,
				Src:  image.Opaque,
				Face: face,
			}
			for i, line := range l.SpeedLines {
				if line == "" {
					continue
				}
				y := 30
				if i == 1 {
					y = 68
				}
				adv := d.MeasureString(line)
				x := fixed.I(hiW-4) - adv
				if x < fixed.I(4) {
					x = fixed.I(4)
				}
				d.Dot = fixed.Point26_6{X: x, Y: fixed.I(y)}
				d.DrawString(line)
			}
			speedFaceMu.Unlock()
			for y := 0; y < 36; y++ {
				for x := 0; x < speedPx; x++ {
					a := (int(hi.AlphaAt(x*2, y*2).A) +
						int(hi.AlphaAt(x*2+1, y*2).A) +
						int(hi.AlphaAt(x*2, y*2+1).A) +
						int(hi.AlphaAt(x*2+1, y*2+1).A) + 2) / 4
					if a > 0 {
						c := col
						c.A = uint8((int(col.A) * a) / 255)
						dst.SetNRGBA(iconPx+x, y, c)
					}
				}
			}
		}
		out = dst
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

// Graph turns the traffic history (bytes/s, oldest first) into bar
// heights: the last GraphBars seconds of up + down, square-root scaled
// against the window's peak (at least 32 KiB/s, so idle noise stays low).
func Graph(up, down []int64) []float64 {
	n := max(len(up), len(down))
	out := make([]float64, GraphBars)
	var peak float64 = 32 << 10
	vals := make([]float64, GraphBars)
	for i := 0; i < GraphBars; i++ {
		j := n - GraphBars + i
		if j < 0 {
			continue
		}
		var v int64
		if j < len(up) {
			v += up[j]
		}
		if j < len(down) {
			v += down[j]
		}
		vals[i] = float64(max(v, 0))
		peak = math.Max(peak, vals[i])
	}
	for i, v := range vals {
		// A few levels only: every different graph is an SVG rasterized
		// and a PNG encoded for the menu bar, once a second while there
		// is traffic. A bar is some 14 points high at most; ten steps
		// are as fine as it shows.
		out[i] = math.Round(math.Sqrt(v/peak)*graphLevels) / graphLevels
	}
	return out
}

// StateOf is the icon state of a status: on when traffic is taken over
// by the system proxy or TUN.
func StateOf(st model.Status) State {
	switch {
	case st.Core == model.CoreError || st.Core == model.CoreMissing:
		return Error
	case st.Core == model.CoreRunning && (st.SystemProxy || st.TUN):
		return On
	case st.Core == model.CoreRunning || st.Core == model.CoreStarting:
		return Half
	}
	return Off
}

// SpeedLinesFor returns the 2-line stacked speed for the macOS menu bar
// icon when both directions are enabled and the core is running.
func SpeedLinesFor(st model.Status, set model.Settings) [2]string {
	if st.Core != model.CoreRunning || set.HideTraySpeed {
		return [2]string{}
	}
	if set.TraySpeed == model.TraySpeedOff || set.TraySpeed == model.TraySpeedDown {
		return [2]string{}
	}
	return [2]string{CompactRate(st.Up), CompactRate(st.Down)}
}

// Title is the text beside the macOS icon, from the settings: mode
// (when not rule), current node, speed, in that order.
func Title(st model.Status, node string, set model.Settings) string {
	return TitleForBar(st, node, set, false)
}

// TitleForBar is Title with stackedSpeed=true when both-direction speed is
// already rendered in two stacked lines inside the tray icon PNG.
func TitleForBar(st model.Status, node string, set model.Settings, stackedSpeed bool) string {
	if st.Core != model.CoreRunning {
		return ""
	}
	var parts []string
	if set.TrayMode && st.Mode != "" && st.Mode != model.ModeRule {
		parts = append(parts, env.ModeName(st.Mode))
	}
	if set.TrayNode && node != "" {
		parts = append(parts, ellipsize(node, 12))
	}
	switch set.TraySpeed {
	case model.TraySpeedOff:
	case model.TraySpeedDown:
		parts = append(parts, "↓"+Rate(st.Down)) // padded: the width stays put
	default:
		if !set.HideTraySpeed && !stackedSpeed {
			parts = append(parts, Speed(st.Up, st.Down))
		}
	}
	return strings.Join(parts, "  ")
}

func ellipsize(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// Speed formats both directions compactly for the menu bar: "↓1.2M ↑ 12K".
// Values are padded with figure spaces so the title keeps its width.
func Speed(up, down int64) string {
	return "↓" + Rate(down) + " ↑" + Rate(up)
}

// Rate is a 4-character rate: "  0B", "512B", "1.5K", " 12K", "999K", "1.2M", " 45M", "1.0G".
func Rate(b int64) string {
	if b < 0 {
		b = 0
	}
	var s string
	switch {
	case b < 1000:
		s = fmt.Sprintf("%dB", b)
	case b < 1<<20:
		s = unit(float64(b)/1024, "K")
	case b < 1<<30:
		s = unit(float64(b)/(1<<20), "M")
	default:
		s = unit(float64(b)/(1<<30), "G")
	}
	for n := len([]rune(s)); n < 4; n++ {
		s = " " + s
	}
	return s
}

func unit(v float64, unit string) string {
	if v < 9.95 {
		return fmt.Sprintf("%.1f%s", v, unit)
	}
	if v > 999 {
		v = 999
	}
	return fmt.Sprintf("%.0f%s", v, unit)
}
