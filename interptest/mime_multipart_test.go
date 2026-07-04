package interptest

import "testing"

// mime/multipart and net/textproto are interpreted from the mirror on wasm (the
// native bridges are dropped there). multipart's Writer pulls crypto/rand for its
// boundary (kept a native bridge on wasm via WasmKeepExact), and its readMIMEHeader
// reaches net/textproto through the ReadMIMEHeaderLimited shim that replaces the
// upstream //go:linkname mvm does not parse. These TestSynth* cases run under the
// wasm CI, so they guard that whole path stays byte-identical.

func TestSynthMimeMultipartRoundTrip(t *testing.T) {
	const src = `package main
import ("bytes"; "fmt"; "io"; "mime/multipart")
func main() {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.SetBoundary("FixedBoundary123")
	fw, _ := w.CreateFormField("name")
	io.WriteString(fw, "gopher")
	ff, _ := w.CreateFormFile("doc", "hello.txt")
	io.WriteString(ff, "alpha\nbeta\n")
	w.Close()
	fmt.Printf("ct=%s len=%d\n", w.FormDataContentType(), buf.Len())

	r := multipart.NewReader(bytes.NewReader(buf.Bytes()), w.Boundary())
	for {
		p, err := r.NextPart()
		if err == io.EOF { break }
		if err != nil { panic(err) }
		body, _ := io.ReadAll(p)
		fmt.Printf("form=%q file=%q body=%q\n", p.FormName(), p.FileName(), string(body))
	}
}`
	want := "ct=multipart/form-data; boundary=FixedBoundary123 len=238\n" +
		"form=\"name\" file=\"\" body=\"gopher\"\n" +
		"form=\"doc\" file=\"hello.txt\" body=\"alpha\\nbeta\\n\"\n"
	if got := evalOut(t, "mpart.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSynthTextprotoMIMEHeader(t *testing.T) {
	const src = `package main
import ("bufio"; "fmt"; "net/textproto"; "strings")
func main() {
	raw := "From: a@b\r\nX-Dup: 1\r\nX-Dup: 2\r\n\r\n"
	r := textproto.NewReader(bufio.NewReader(strings.NewReader(raw)))
	h, err := r.ReadMIMEHeader()
	if err != nil { panic(err) }
	fmt.Printf("from=%v dup=%v\n", h["From"], h["X-Dup"])
}`
	want := "from=[a@b] dup=[1 2]\n"
	if got := evalOut(t, "tproto.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The godebug overlay must match a '#'-prefixed (undocumented) setting against
// its env key, which omits the '#'; multipart's multipartfiles=distinct toggle
// silently no-op'd (wasm mime/multipart TestReadForm_ManyFiles_Distinct).
func TestSynthGodebugUndocumentedName(t *testing.T) {
	const src = `package main
import ("fmt"; "internal/godebug"; "os")
func main() {
	os.Setenv("GODEBUG", "foo=bar")
	s := godebug.New("#foo")
	fmt.Println(s.Name(), s.Value())
}`
	want := "foo bar\n"
	if got := evalOut(t, "godebughash.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
