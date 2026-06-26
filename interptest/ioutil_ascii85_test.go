package interptest

import "testing"

// io/ioutil: mirrored on wasm, bridged on native. The bridge forwards ReadAll
// to io.ReadAll, which loops calling r.Read; on wasm io is interpreted, so the
// native loop traps on a synth receiver. Mirroring keeps the chain interpreted.
func TestSynthIoutil(t *testing.T) {
	const src = `package main
import ("fmt"; "io"; "io/ioutil")
type myReader struct{ s string; i int }
func (r *myReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) { return 0, io.EOF }
	n := copy(p, r.s[r.i:]); r.i += n; return n, nil
}
func main() {
	b, err := ioutil.ReadAll(&myReader{s: "hello ioutil"})
	fmt.Println("ReadAll:", string(b), err)
	rc := ioutil.NopCloser(&myReader{s: "nopclose"})
	b2, _ := ioutil.ReadAll(rc)
	fmt.Println("NopCloser:", string(b2))
	n, _ := io.Copy(ioutil.Discard, &myReader{s: "discarded"})
	fmt.Println("Discard:", n)
}`
	want := "ReadAll: hello ioutil <nil>\nNopCloser: nopclose\nDiscard: 9\n"
	if got := evalOut(t, "ioutil.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// encoding/ascii85: mirrored on wasm (was absent), bridged on native.
func TestSynthAscii85(t *testing.T) {
	const src = `package main
import ("encoding/ascii85"; "fmt")
func main() {
	src := []byte("Man is distinguished")
	enc := make([]byte, ascii85.MaxEncodedLen(len(src)))
	enc = enc[:ascii85.Encode(enc, src)]
	fmt.Println("enc:", string(enc))
	dec := make([]byte, len(src))
	n, _, _ := ascii85.Decode(dec, enc, true)
	fmt.Println("dec:", string(dec[:n]))
}`
	want := "enc: 9jqo^BlbD-BleB1DJ+*+F(f,q\ndec: Man is distinguished\n"
	if got := evalOut(t, "ascii85.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
