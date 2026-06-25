package interptest

import "testing"

// index/suffixarray is interpreted from the mirror on wasm (the native bridge is
// dropped via WasmDropExact). Deps (bytes/encoding-binary/io/math/regexp/slices/
// sort) are all mirrored. This TestSynth* case runs under the wasm CI: native =
// bridge path, wasm = mirror path.

func TestSynthSuffixArray(t *testing.T) {
	const src = `package main
import ("bytes"; "fmt"; "index/suffixarray"; "regexp"; "sort")
func main() {
	data := []byte("banana bandana")
	idx := suffixarray.New(data)
	off := idx.Lookup([]byte("ana"), -1)
	sort.Ints(off) // Lookup order is unspecified
	fmt.Println(off)
	fmt.Println(idx.FindAllIndex(regexp.MustCompile("an+a"), -1))
	var buf bytes.Buffer
	idx.Write(&buf)
	var idx2 suffixarray.Index
	idx2.Read(&buf)
	o2 := idx2.Lookup([]byte("ana"), -1)
	sort.Ints(o2)
	fmt.Println(o2, bytes.Equal(idx2.Bytes(), data))
}`
	want := "[1 3 11]\n[[1 4] [11 14]]\n[1 3 11] true\n"
	if got := evalOut(t, "sufa.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
