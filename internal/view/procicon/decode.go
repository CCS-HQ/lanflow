package procicon

import (
	"bytes"
	"errors"
	"image"
	"image/color"
)

// MaxPixels is the largest picture DecodeImage decodes, about 2048²:
// image bytes say nothing of the picture's size (a few hundred kilobytes
// of PNG can hold 20000×20000 pixels of one colour, gigabytes once
// decoded), so the header is read first and a larger picture refused.
const MaxPixels = 2048 * 2048

// ErrTooLarge is DecodeImage's error for a picture over MaxPixels.
var ErrTooLarge = errors.New("procicon: image too large to decode")

// DecodeImage decodes the image in data (the formats registered with
// package image by the caller's imports), refusing one of more than
// MaxPixels, and scales it down so that its longer edge is at most edge
// pixels (edge <= 0: as it is). Icons shown at 16 DIPs and previews shown
// in a panel need no more than two or three times their size on screen.
// It is safe from any goroutine.
func DecodeImage(data []byte, edge int) (image.Image, error) {
	return DecodeImageMax(data, edge, MaxPixels)
}

// DecodeImageMax is DecodeImage with its own limit of pixels, for
// pictures that are content rather than icons (a photograph in a
// captured response).
func DecodeImageMax(data []byte, edge, maxPixels int) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxPixels || cfg.Height > maxPixels || cfg.Width*cfg.Height > maxPixels {
		return nil, ErrTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return shrink(img, edge), nil
}

// shrink returns img scaled down so that its longer edge is edge pixels,
// averaging the pixels each new one covers; img itself when it is small
// enough already.
func shrink(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if edge <= 0 || (w <= edge && h <= edge) || w <= 0 || h <= 0 {
		return img
	}
	nw, nh := edge, edge
	if w >= h {
		nh = max(1, h*edge/w)
	} else {
		nw = max(1, w*edge/h)
	}
	out := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			// Average with the alpha premultiplied, as RGBA() gives it,
			// so that transparent pixels do not darken the edges.
			var r, g, bl, a, n uint64
			for sy := y0; sy < max(y1, y0+1); sy++ {
				for sx := x0; sx < max(x1, x0+1); sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			out.Set(x, y, color.RGBA64{R: uint16(r / n), G: uint16(g / n), B: uint16(bl / n), A: uint16(a / n)})
		}
	}
	return out
}
