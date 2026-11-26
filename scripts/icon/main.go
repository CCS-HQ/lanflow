// Command icon draws resources/icon.png, the app icon MyGo bundles for
// every platform, from its source scripts/icon/icon.svg:
//
//	go run ./scripts/icon
package main

import (
	"bytes"
	"image/png"
	"log"
	"os"

	"github.com/egoist/mygo/ui"
)

func main() {
	src, err := os.ReadFile("scripts/icon/icon.svg")
	if err != nil {
		log.Fatal(err)
	}
	s, err := ui.ParseSVG(src)
	if err != nil {
		log.Fatal(err)
	}
	if u := s.Unsupported(); len(u) > 0 {
		log.Fatalf("icon.svg uses what the renderer does not draw: %v", u)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, s.Image(1024, 1024, nil)); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("resources/icon.png", b.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}
