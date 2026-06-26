package interptest

import "testing"

// testing/iotest: mirrored on wasm, bridged on native. Its wrappers (OneByteReader,
// TestReader) drive the wrapped Reader's Read; on wasm the reader is interpreted,
// so a native bridge would trap on the synth receiver. Mirroring keeps it interpreted.
func TestSynthIotest(t *testing.T) {
	const src = `package main
import ("fmt"; "io"; "strings"; "testing/iotest")
func main() {
	r := iotest.OneByteReader(strings.NewReader("abcde"))
	b, _ := io.ReadAll(r)
	fmt.Println("OneByte:", string(b))
	if err := iotest.TestReader(strings.NewReader("xyz"), []byte("xyz")); err != nil {
		fmt.Println("TestReader:", err)
	} else {
		fmt.Println("TestReader: ok")
	}
}`
	want := "OneByte: abcde\nTestReader: ok\n"
	if got := evalOut(t, "iotest.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// testing/fstest: mirrored on wasm, bridged on native. MapFS implements fs.FS and
// TestFS validates it by dispatching the FS's interface methods; on wasm those are
// interpreted, so a native bridge traps. Mirroring also exercised the unifyTP
// element-from-Rtype fix (slices.Sort over Glob/Keys results); see
// TestInferBridgeSliceElem.
func TestSynthFstest(t *testing.T) {
	const src = `package main
import ("fmt"; "io/fs"; "testing/fstest")
func main() {
	m := fstest.MapFS{
		"a.txt":   {Data: []byte("hello")},
		"d/b.txt": {Data: []byte("world")},
	}
	data, _ := fs.ReadFile(m, "a.txt")
	fmt.Println("read:", string(data))
	var names []string
	fs.WalkDir(m, ".", func(p string, d fs.DirEntry, err error) error {
		names = append(names, p)
		return nil
	})
	fmt.Println("walk:", names)
	if err := fstest.TestFS(m, "a.txt", "d/b.txt"); err != nil {
		fmt.Println("TestFS:", err)
	} else {
		fmt.Println("TestFS: ok")
	}
}`
	want := "read: hello\nwalk: [. a.txt d d/b.txt]\nTestFS: ok\n"
	if got := evalOut(t, "fstest.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
