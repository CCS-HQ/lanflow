package capture

import (
	"bytes"
	"encoding/json"
	"image"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/capture/redact"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
)

// The 请求体 / 响应体 tabs. Decoding a body (gzip up to 16 MiB), masking
// it, indenting its JSON and decoding its picture are done once, off the
// UI thread, and kept with the detail: the page is rebuilt several times
// a second while requests arrive, and on every keystroke.

const previewLimit = 256 << 10

// Pictures in bodies are content, not icons: larger ones are decoded than
// procicon.MaxPixels allows, and kept at most previewEdge pixels a side,
// twice the widest the panel shows them.
const (
	previewPixels = 4096 * 4096
	previewEdge   = 2048
)

// bodyKey names a preview of the detail held: of the request's or the
// response's body, masked or not.
type bodyKey struct{ resp, reveal bool }

// bodyPreview is what a body tab shows of a body.
type bodyPreview struct {
	enc    string // the Content-Encoding, when it could not be undone
	failed bool   // the encoding could not be undone
	kind   string // redact.Kind
	size   int    // bytes once decoded
	hex    string // of a binary body
	text   string // of the others, masked unless revealed
	// clipped: text is the first previewLimit bytes only; unmasked: the
	// text is of a kind masking does not cover.
	clipped, unmasked bool
	// img is the decoded picture, which the view turns into src (a
	// bitmap is made on the UI thread); tooLarge tells why there is none.
	img      image.Image
	src      ui.ImageSource
	tooLarge bool
}

// makePreview builds the preview of body with headers h. It touches
// nothing of the page: safe from any goroutine.
func makePreview(body []byte, h []model.KV, reveal bool) *bodyPreview {
	pv := &bodyPreview{enc: header(h, "content-encoding")}
	dec, ok := redact.Decode(body, pv.enc)
	if !ok {
		pv.failed = true
		return pv
	}
	ct := header(h, "content-type")
	pv.kind, pv.size = redact.Kind(ct, dec), len(dec)
	switch pv.kind {
	case "image":
		img, err := procicon.DecodeImageMax(dec, previewEdge, previewPixels)
		pv.img, pv.tooLarge = img, err == procicon.ErrTooLarge
		return pv
	case "binary":
		pv.hex = hexdump(dec, 512)
		return pv
	}
	show := dec
	if !reveal {
		show = redact.Body(dec, ct)
	} else if pv.kind == "json" {
		var b bytes.Buffer
		if json.Indent(&b, dec, "", "  ") == nil {
			show = b.Bytes()
		}
	}
	if pv.kind == "form" {
		show = []byte(strings.ReplaceAll(string(show), "&", "\n"))
	}
	if len(show) > previewLimit {
		show, pv.clipped = show[:previewLimit], true
	}
	pv.unmasked = !reveal && (pv.kind == "text" || pv.kind == "multipart")
	pv.text = string(bytes.ToValidUTF8(show, []byte("�")))
	return pv
}

// previewCache keeps the previews of the bodies of one detail or one
// replayed answer. Its goroutines write under mu; the view reads there.
type previewCache struct {
	mu   sync.Mutex
	gen  uint64 // counts the resets: a preview made for older bodies is dropped
	done map[bodyKey]*bodyPreview
	busy map[bodyKey]bool
}

// reset drops the previews, as for new bodies, and takes done (which may
// be nil) as the ones already made of them.
func (pc *previewCache) reset(done map[bodyKey]*bodyPreview) {
	pc.mu.Lock()
	pc.gen++
	pc.done, pc.busy = done, nil
	pc.mu.Unlock()
}

// get returns the preview for key, nil while it is being made: the first
// ask starts a goroutine that makes it and then calls redraw.
func (pc *previewCache) get(key bodyKey, body []byte, h []model.KV, redraw func()) *bodyPreview {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pv, ok := pc.done[key]; ok {
		return pv
	}
	if pc.busy[key] {
		return nil
	}
	if pc.busy == nil {
		pc.busy = map[bodyKey]bool{}
	}
	pc.busy[key] = true
	gen := pc.gen
	go func() {
		pv := makePreview(body, h, key.reveal)
		pc.mu.Lock()
		if pc.gen == gen {
			if pc.done == nil {
				pc.done = map[bodyKey]*bodyPreview{}
			}
			pc.done[key] = pv
			delete(pc.busy, key)
		}
		pc.mu.Unlock()
		if redraw != nil {
			redraw()
		}
	}()
	return nil
}

// bodyView is a body tab of the detail: d's request or response body, as
// the reveal switch has it. The detail's fetch made the two previews of
// the switch's state then; a flip of the switch makes the others.
func (p *Page) bodyView(c *ui.Context, t *ui.Theme, d model.CaptureFlowDetail, resp bool) {
	body, size, h := d.ReqBody, d.ReqBodySize, d.ReqHeader
	if resp {
		body, size, h = d.RespBody, d.RespBodySize, d.RespHeader
	}
	p.showBody(c, t, &p.bodies, bodyKey{resp: resp, reveal: p.reveal}, body, size, h)
}

// showBody draws a body of size bytes, of which body was recorded, from
// its preview in pc.
func (p *Page) showBody(c *ui.Context, t *ui.Theme, pc *previewCache, key bodyKey, body []byte, size int64, h []model.KV) {
	if size == 0 && len(body) == 0 {
		ui.Text(c, i18n.T("无正文")).FontSize(12).TextColor(t.TextMuted)
		return
	}
	if size > int64(len(body)) {
		ui.Text(c, i18n.Tf("已截断：记录了 %s，原大小 %s", env.FmtBytes(int64(len(body))), env.FmtBytes(size))).FontSize(11).TextColor(t.TextMuted)
	}
	pv := pc.get(key, body, h, p.e.Redraw)
	switch {
	case pv == nil:
		ui.Spinner(c).Size(14, 14).Label(i18n.T("加载中"))
	case pv.failed:
		ui.Text(c, i18n.Tf("正文以 %s 压缩，无法预览（%s）", pv.enc, env.FmtBytes(int64(len(body))))).FontSize(12).TextColor(t.TextMuted)
	case pv.kind == "image":
		p.image(c, t, pv)
	case pv.kind == "binary":
		ui.Text(c, i18n.Tf("二进制内容（%s）", env.FmtBytes(int64(pv.size)))).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, pv.hex).FontSize(11).Font("monospace")
	default:
		if pv.clipped {
			ui.Text(c, i18n.T("只显示前 256 KiB")).FontSize(11).TextColor(t.TextMuted)
		}
		if pv.unmasked {
			ui.Text(c, i18n.T("文本正文不做打码，可能包含凭据。")).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Text(c, pv.text).FontSize(12).Font("monospace")
	}
}

// image shows a body that is a picture. Each body has its own preview, so
// a request and a response that are both pictures show their own.
func (p *Page) image(c *ui.Context, t *ui.Theme, pv *bodyPreview) {
	if pv.src == nil && pv.img != nil {
		// The picture was decoded and scaled off the UI thread; only the
		// conversion to a bitmap is left for here.
		pv.src, pv.img = ui.NewBitmap(pv.img), nil
	}
	switch {
	case pv.src != nil:
		// At its own size, scaled down to the panel's width, never up.
		img := ui.Image(c, pv.src).MaxWidthPercent(100).Radius(4)
		if w, h := ui.ImageSize(pv.src); w > 0 && h > 0 {
			img.AspectRatio(w / h)
		}
	case pv.tooLarge:
		ui.Text(c, i18n.Tf("图片（%s）尺寸过大，不预览", env.FmtBytes(int64(pv.size)))).FontSize(12).TextColor(t.TextMuted)
	default:
		ui.Text(c, i18n.Tf("图片（%s），无法预览此格式", env.FmtBytes(int64(pv.size)))).FontSize(12).TextColor(t.TextMuted)
	}
}
