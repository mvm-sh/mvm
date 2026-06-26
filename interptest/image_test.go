package interptest

import "testing"

// image/* is mirrored on wasm (was dropped via the "image" WasmDrop prefix),
// bridged on native. The codecs consume io.Reader/Writer and image/draw
// dispatches At/Set on the image.Image interface; mirroring keeps the chain
// interpreted. PNG is lossless so decoded pixels are asserted exactly; JPEG/GIF
// are checked structurally.
func TestSynthImage(t *testing.T) {
	const src = `package main
import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
)
func main() {
	m := image.NewRGBA(image.Rect(0, 0, 4, 4))
	draw.Draw(m, m.Bounds(), &image.Uniform{color.RGBA{10, 20, 30, 255}}, image.Point{}, draw.Src)
	m.Set(1, 1, color.RGBA{200, 100, 50, 255})
	r, g, b, a := m.At(1, 1).RGBA()
	fmt.Printf("pixel %d %d %d %d\n", r>>8, g>>8, b>>8, a>>8)

	var pbuf bytes.Buffer
	if err := png.Encode(&pbuf, m); err != nil { fmt.Println("png enc", err); return }
	encoded := pbuf.Bytes()
	pm, err := png.Decode(bytes.NewReader(encoded))
	if err != nil { fmt.Println("png dec", err); return }
	pr, pg, pb, _ := pm.At(0, 0).RGBA()
	fmt.Printf("png (0,0) %d %d %d\n", pr>>8, pg>>8, pb>>8)

	_, format, _ := image.DecodeConfig(bytes.NewReader(encoded))
	fmt.Println("format", format)

	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, m, &jpeg.Options{Quality: 90}); err != nil { fmt.Println("jpeg enc", err); return }
	jm, err := jpeg.Decode(&jbuf)
	if err != nil { fmt.Println("jpeg dec", err); return }
	fmt.Println("jpeg", jm.Bounds())

	var gbuf bytes.Buffer
	if err := gif.Encode(&gbuf, m, nil); err != nil { fmt.Println("gif enc", err); return }
	gm, err := gif.Decode(&gbuf)
	if err != nil { fmt.Println("gif dec", err); return }
	fmt.Println("gif", gm.Bounds())
}`
	want := "pixel 200 100 50 255\n" +
		"png (0,0) 10 20 30\n" +
		"format png\n" +
		"jpeg (0,0)-(4,4)\n" +
		"gif (0,0)-(4,4)\n"
	if got := evalOut(t, "image.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
