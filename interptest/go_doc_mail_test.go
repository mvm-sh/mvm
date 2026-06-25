package interptest

import "testing"

// go/doc and net/mail are interpreted from the mirror on wasm (native bridges
// dropped under the "go"/"net" WasmDrop prefixes). net/mail's domain-literal IP
// check uses net/netip on the mirror (net is absent on wasm; the patch matches
// net.ParseIP, which rejects a zone). go/doc pulls internal/lazyregexp (mirrored).
// These TestSynth* cases run under the wasm CI: native = bridge, wasm = mirror.

func TestSynthGoDoc(t *testing.T) {
	const src = "package main\n" +
		`import ("fmt"; "go/ast"; "go/doc"; "go/parser"; "go/token")
func main() {
	const code = "// Package p does things.\npackage p\n\n// F runs F.\nfunc F() {}\n"
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "p.go", code, parser.ParseComments)
	pkg, _ := doc.NewFromFiles(fset, []*ast.File{f}, "example.com/p")
	fmt.Printf("name=%s syn=%q func=%s:%q\n", pkg.Name, doc.Synopsis(pkg.Doc), pkg.Funcs[0].Name, pkg.Funcs[0].Doc)
}`
	want := "name=p syn=\"Package p does things.\" func=F:\"F runs F.\\n\"\n"
	if got := evalOut(t, "godoc.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSynthNetMail(t *testing.T) {
	const src = `package main
import ("fmt"; "net/mail")
func main() {
	a, _ := mail.ParseAddress("Bob <bob@example.org>")
	fmt.Printf("%q %q\n", a.Name, a.Address)
	for _, in := range []string{"u@[192.168.0.1]", "u@[::1]", "u@[fe80::1%eth0]", "u@[bad]"} {
		_, err := mail.ParseAddress(in)
		fmt.Printf("%s ok=%v\n", in, err == nil)
	}
}`
	want := "\"Bob\" \"bob@example.org\"\n" +
		"u@[192.168.0.1] ok=true\n" +
		"u@[::1] ok=true\n" +
		"u@[fe80::1%eth0] ok=false\n" +
		"u@[bad] ok=false\n"
	if got := evalOut(t, "netmail.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
