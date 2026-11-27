package view

import "github.com/egoist/mygo/ui"

// svg wraps 16×16 line-icon shapes in an SVG that takes the text color.
func svg(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

// icons are the monochrome line icons by the names env.Kit.Icon takes.
var icons = map[string]*ui.SVG{
	"overview": svg(`<path d="M2.5 11.5a5.5 5.5 0 1 1 11 0"/><path d="M8 11.5l2.6-3.2"/><circle cx="8" cy="11.5" r=".6" fill="currentColor"/>`),
	"proxies":  svg(`<circle cx="8" cy="8" r="6"/><path d="M2 8h12M8 2c1.8 1.8 2.6 3.8 2.6 6S9.8 12.2 8 14C6.2 12.2 5.4 10.2 5.4 8S6.2 3.8 8 2z"/>`),
	"subs":     svg(`<rect x="4.5" y="2.5" width="9" height="11" rx="1.5"/><path d="M2.5 5v8a1.5 1.5 0 0 0 1.5 1.5h7M7 6h4M7 8.5h4"/>`),
	"conns":    svg(`<path d="M5 13V3M2.5 5.5L5 3l2.5 2.5M11 3v10M8.5 10.5L11 13l2.5-2.5"/>`),
	"logs":     svg(`<path d="M2.5 3.5h11M2.5 6.5h11M2.5 9.5h7M2.5 12.5h9"/>`),
	"rules":    svg(`<path d="M2.5 4h1.5M2.5 8h1.5M2.5 12h1.5M6.5 4h7M6.5 8h7M6.5 12h4.5"/>`),
	"net":      svg(`<path d="M1.5 6.5a9.2 9.2 0 0 1 13 0M3.8 8.8a6 6 0 0 1 8.4 0M6 11a2.8 2.8 0 0 1 4 0"/><circle cx="8" cy="13" r=".8" fill="currentColor"/>`),
	"stats":    svg(`<path d="M2.5 13.5h11"/><path d="M4 13.5V8.5M7 13.5V4.5M10 13.5V7M13 13.5V2.5" stroke-width="1.8"/>`),
	"settings": svg(`<circle cx="8" cy="8" r="2.2"/><path d="M6.9 1.8H9.1L9.5 3.6L10.6 4.1L11.9 3L13 4.1L11.9 5.4L12.4 6.5L14.2 6.9V9.1L12.4 9.5L11.9 10.6L13 11.9L11.9 13L10.6 11.9L9.5 12.4L9.1 14.2H6.9L6.5 12.4L5.4 11.9L4.1 13L3 11.9L4.1 10.6L3.6 9.5L1.8 9.1V6.9L3.6 6.5L4.1 5.4L3 4.1L4.1 3L5.4 4.1L6.5 3.6Z"/>`),
	"tun":      svg(`<rect x="2" y="3" width="12" height="9" rx="1.8"/><path d="M5.5 14h5M8 12v2M8.4 5.2L6.6 7.8h2l-.8 2.4 2.2-3H8l.4-2z"/>`),
	"search":   svg(`<circle cx="7" cy="7" r="4.2"/><path d="M10.2 10.2l3.3 3.3"/>`),
	"more":     svg(`<circle cx="3.5" cy="8" r="1" fill="currentColor"/><circle cx="8" cy="8" r="1" fill="currentColor"/><circle cx="12.5" cy="8" r="1" fill="currentColor"/>`),
	"plus":     svg(`<path d="M8 3v10M3 8h10"/>`),
	"refresh":  svg(`<path d="M13 8a5 5 0 1 1-1.5-3.6M13 2.5v3h-3"/>`),
	"bolt":     svg(`<path d="M9 1.8L3.8 9h4l-1 5.2L12.2 7h-4z"/>`),
	"pause":    svg(`<path d="M5.5 3.5v9M10.5 3.5v9"/>`),
	"play":     svg(`<path d="M5 3.2l7.5 4.8L5 12.8z"/>`),
	"copy":     svg(`<rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M10.5 3.5V3A1.5 1.5 0 0 0 9 1.5H4A1.5 1.5 0 0 0 2.5 3v5A1.5 1.5 0 0 0 4 9.5h.5"/>`),
	"close":    svg(`<path d="M4 4l8 8M12 4l-8 8"/>`),
	"down":     svg(`<path d="M8 3v10M4 9l4 4 4-4"/>`),
	"warn":     svg(`<path d="M8 2l6.5 11.5h-13z"/><path d="M8 6.5v3.2"/><circle cx="8" cy="11.6" r=".5" fill="currentColor"/>`),
	"check":    svg(`<path d="M3 8.5l3.2 3L13 4.5"/>`),
	"doc":      svg(`<rect x="3.5" y="1.5" width="9" height="13" rx="1.5"/><path d="M6 5h4M6 8h4M6 11h2"/>`),
	"power":    svg(`<path d="M8 1.8v6M4.4 4.2a5 5 0 1 0 7.2 0"/>`),
	"link":     svg(`<path d="M7 9a3 3 0 0 0 4.2.2l2-2a3 3 0 0 0-4.2-4.3l-1 1M9 7a3 3 0 0 0-4.2-.2l-2 2a3 3 0 0 0 4.2 4.3l1-1"/>`),
	"info":     svg(`<circle cx="8" cy="8" r="6"/><path d="M8 7.2v4"/><circle cx="8" cy="5" r=".6" fill="currentColor"/>`),
	"chevron":  svg(`<path d="M6 3.5L10.5 8 6 12.5"/>`),
	"probe":    svg(`<path d="M8 1.8l5 1.9v4.1c0 3.2-2.1 5.3-5 6.4-2.9-1.1-5-3.2-5-6.4V3.7z"/><path d="M5.8 8.2l1.6 1.6 3-3.2"/>`),
	"filter":   svg(`<path d="M2.5 4h11M4.5 8h7M6.5 12h3"/>`),
	"up":       svg(`<path d="M8 13V3M4 7l4-4 4 4"/>`),
	"capture":  svg(`<rect x="3.5" y="7" width="9" height="7" rx="1.5"/><path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2"/><path d="M8 9.6v1.8"/>`),
	"locate":   svg(`<circle cx="8" cy="8" r="4.5"/><circle cx="8" cy="8" r="1" fill="currentColor"/><path d="M8 1.5v3M8 11.5v3M1.5 8h3M11.5 8h3"/>`),
	"sidebar":  svg(`<rect x="2" y="2.5" width="12" height="11" rx="2"/><path d="M6 2.5v11"/>`),
	// The aggregate subscription editor (subs/aggregate*.go).
	"edit":    svg(`<path d="M10.5 2.5l3 3-8 8H2.5v-3z"/><path d="M9 4l3 3"/>`),
	"trash":   svg(`<path d="M2.5 4h11M6 4V2.5h4V4M4 4l.7 9.5h6.6L12 4M6.8 6.5v4.5M9.2 6.5v4.5"/>`),
	"expand":  svg(`<path d="M9.5 2.5h4v4M13.5 2.5L9 7M6.5 13.5h-4v-4M2.5 13.5L7 9"/>`),
	"server":  svg(`<rect x="2.5" y="2.5" width="11" height="4.5" rx="1.2"/><rect x="2.5" y="9" width="11" height="4.5" rx="1.2"/><path d="M5 4.75h.01M5 11.25h.01"/>`),
	"eye":     svg(`<path d="M1.5 8s2.4-4.5 6.5-4.5S14.5 8 14.5 8 12.1 12.5 8 12.5 1.5 8 1.5 8z"/><circle cx="8" cy="8" r="2"/>`),
	"eye-off": svg(`<path d="M2.5 2.5l11 11M6.6 4.3A6.4 6.4 0 0 1 8 3.5c4.1 0 6.5 4.5 6.5 4.5a11 11 0 0 1-1.9 2.4M11 11.6A6.3 6.3 0 0 1 8 12.5C3.9 12.5 1.5 8 1.5 8a11.3 11.3 0 0 1 2.7-3.1M6.6 6.6a2 2 0 0 0 2.8 2.8"/>`),
	"layers":  svg(`<path d="M8 2l6 3.2-6 3.2-6-3.2z"/><path d="M2 8.2l6 3.2 6-3.2M2 11l6 3.2 6-3.2"/>`),
	"sliders": svg(`<path d="M2.5 4.5h6M11.5 4.5h2M2.5 11.5h2M7.5 11.5h6"/><circle cx="10" cy="4.5" r="1.5"/><circle cx="6" cy="11.5" r="1.5"/>`),
	"upload":  svg(`<path d="M8 10.5V2.5M5 5.5l3-3 3 3M2.5 10.5v2a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1v-2"/>`),
	"graph":   svg(`<rect x="6" y="1.5" width="4" height="3.5" rx=".8"/><rect x="1.5" y="11" width="4" height="3.5" rx=".8"/><rect x="10.5" y="11" width="4" height="3.5" rx=".8"/><path d="M8 5v3M3.5 11V8h9v3"/>`),
	"grip":    svg(`<circle cx="6" cy="4" r=".7" fill="currentColor"/><circle cx="10" cy="4" r=".7" fill="currentColor"/><circle cx="6" cy="8" r=".7" fill="currentColor"/><circle cx="10" cy="8" r=".7" fill="currentColor"/><circle cx="6" cy="12" r=".7" fill="currentColor"/><circle cx="10" cy="12" r=".7" fill="currentColor"/>`),
}

// pageIcons maps sidebar pages to their icon names.
var pageIcons = map[string]string{
	"overview": "overview", "proxies": "proxies", "subs": "subs", "conns": "conns",
	"logs": "logs", "rules": "rules", "net": "net", "stats": "stats", "probe": "probe", "capture": "capture", "aggregate": "layers", "settings": "settings",
}

// Icon returns the SVG of a named icon, nil if unknown.
func Icon(name string) *ui.SVG { return icons[name] }
