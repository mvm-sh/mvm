package interptest

import "testing"

// compress/* and archive/* are interpreted from the mirror on wasm (the native
// bridges are dropped under the compress/archive WasmDrop prefixes). They CONSUME
// io.Reader/Writer, which are interpreted on wasm, so a native bridge would trap
// (native code dispatching an interpreted Read/Write through the shared-PC
// sentinel) -- hence mirroring, not bridging. The interpreted compressors call the
// native hash/crc32 + hash/adler32 bridges (interp-calls-native, no trap). These
// TestSynth* cases run under the wasm CI: native = bridge path, wasm = mirror path.

func TestSynthCompress(t *testing.T) {
	const src = `package main
import (
	"bytes"
	"compress/bzip2"
	"compress/flate"
	"compress/gzip"
	"compress/lzw"
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"io"
)
func main() {
	data := []byte("the quick brown fox jumps over the lazy dog, the quick brown fox\n")
	eq := func(b []byte) bool { return string(b) == string(data) }

	var fb bytes.Buffer
	fw, _ := flate.NewWriter(&fb, flate.BestCompression)
	fw.Write(data); fw.Close()
	fo, _ := io.ReadAll(flate.NewReader(bytes.NewReader(fb.Bytes())))
	fmt.Printf("flate clen=%d ok=%v\n", fb.Len(), eq(fo))

	var gb bytes.Buffer
	gw := gzip.NewWriter(&gb)
	gw.Write(data); gw.Close()
	gr, _ := gzip.NewReader(bytes.NewReader(gb.Bytes()))
	go_, _ := io.ReadAll(gr)
	fmt.Printf("gzip clen=%d head=%s ok=%v\n", gb.Len(), hex.EncodeToString(gb.Bytes()[:4]), eq(go_))

	var zb bytes.Buffer
	zw := zlib.NewWriter(&zb)
	zw.Write(data); zw.Close()
	zr, _ := zlib.NewReader(bytes.NewReader(zb.Bytes()))
	zo, _ := io.ReadAll(zr)
	fmt.Printf("zlib clen=%d ok=%v\n", zb.Len(), eq(zo))

	var lb bytes.Buffer
	lw := lzw.NewWriter(&lb, lzw.LSB, 8)
	lw.Write(data); lw.Close()
	lo, _ := io.ReadAll(lzw.NewReader(bytes.NewReader(lb.Bytes()), lzw.LSB, 8))
	fmt.Printf("lzw clen=%d ok=%v\n", lb.Len(), eq(lo))

	blob, _ := hex.DecodeString("425a68393141592653593157e9940000125180001040003ffffff0200022a7a688309a686d1b5051a1a000003990f045093d854aac56db0c53f89a2c714c1f7753b814db39d0bb9229c284818abf4ca0")
	bo, _ := io.ReadAll(bzip2.NewReader(bytes.NewReader(blob)))
	fmt.Printf("bzip2 %q\n", string(bo))
}`
	want := "flate clen=52 ok=true\n" +
		"gzip clen=70 head=1f8b0800 ok=true\n" +
		"zlib clen=58 ok=true\n" +
		"lzw clen=62 ok=true\n" +
		"bzip2 \"the quick brown fox jumps over the lazy dog\\n\"\n"
	if got := evalOut(t, "compress.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSynthArchive(t *testing.T) {
	const src = `package main
import ("archive/tar"; "archive/zip"; "bytes"; "fmt"; "io")
func main() {
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	tw.WriteHeader(&tar.Header{Name: "a.txt", Mode: 0o644, Size: 5})
	tw.Write([]byte("alpha"))
	tw.Close()
	tr := tar.NewReader(bytes.NewReader(tb.Bytes()))
	h, _ := tr.Next()
	body, _ := io.ReadAll(tr)
	fmt.Printf("tar %s mode=%o size=%d %q\n", h.Name, h.Mode, h.Size, string(body))

	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.Create("hello.txt")
	f.Write([]byte("hello zip"))
	zw.Close()
	zr, _ := zip.NewReader(bytes.NewReader(zb.Bytes()), int64(zb.Len()))
	rc, _ := zr.File[0].Open()
	zd, _ := io.ReadAll(rc)
	rc.Close()
	fmt.Printf("zip %s usize=%d %q\n", zr.File[0].Name, zr.File[0].UncompressedSize64, string(zd))
}`
	want := "tar a.txt mode=644 size=5 \"alpha\"\n" +
		"zip hello.txt usize=9 \"hello zip\"\n"
	if got := evalOut(t, "archive.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
