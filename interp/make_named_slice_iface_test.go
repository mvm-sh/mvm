package interp

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// make(NamedSlice, n) must keep its named identity at runtime. Before the fix
// MkSlice rebuilt the value as the unnamed underlying []T, so boxing the make
// result straight into an interface (here a struct field round-tripped through
// a channel) dropped NamedSlice's method set and method dispatch nil-dereffed.
// Mirrors grpc internal/transport TestReadMessageHeaderMultipleBuffers, where a
// mem.SliceBuffer flows through recvBuffer's recvMsg channel and then dispatches
// the unexported mem.Buffer.read method inside package mem.
func TestMakeNamedSliceIfaceIdentity(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/mem",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/mem\n",
			"mem.go": `package mem

type Buffer interface {
	Len() int
	read(buf []byte) (int, Buffer)
}

type SliceBuffer []byte

func (s SliceBuffer) Len() int { return len(s) }

func (s SliceBuffer) read(buf []byte) (int, Buffer) {
	n := copy(buf, s)
	if n == len(s) {
		return n, nil
	}
	return n, s[n:]
}

func ReadUnsafe(dst []byte, buf Buffer) (int, Buffer) {
	return buf.read(dst)
}
`,
		},
	})

	src := `package main

import "example.com/x/mem"

type msg struct {
	buf mem.Buffer
	err error
}

func main() {
	ch := make(chan msg, 1)
	ch <- msg{buf: make(mem.SliceBuffer, 3)}
	m := <-ch
	dst := make([]byte, 2)
	n, rest := mem.ReadUnsafe(dst, m.buf)
	println("n=", n, "rest!=nil:", rest != nil)
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got := stderr.String(); strings.Contains(got, "panic") {
		t.Fatalf("dispatch panicked: %s", got)
	}
	if got, want := stdout.String(), "n= 2 rest!=nil: true\n"; got != want {
		t.Errorf("output: got %q, want %q", got, want)
	}
}
