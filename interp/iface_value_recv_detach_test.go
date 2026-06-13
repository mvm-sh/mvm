package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A value-receiver method dispatched through an embedded interface field got
// the unboxed iface value as its receiver cell; the value is unaddressable,
// so the first field write panicked "reflect.Value.Set using unaddressable
// value" (zerolog ConsoleWriter.Write: w.PartsOrder = ...). The cell is now
// detached to an addressable copy, which also matches Go's copy semantics.
func TestIfaceValueRecvFieldWrite(t *testing.T) {
	src := `package main

import (
	"bytes"
	"fmt"
	"io"
)

type W struct {
	Out   io.Writer
	Parts []string
}

func (w W) Write(p []byte) (int, error) {
	if w.Parts == nil {
		w.Parts = []string{"a", "b"}
	}
	return len(w.Parts), nil
}

type LevelWriter interface {
	io.Writer
	WriteLevel(level int, p []byte) (int, error)
}

type Adapter struct {
	io.Writer
}

func (lw Adapter) WriteLevel(_ int, p []byte) (int, error) {
	return lw.Write(p)
}

type Logger struct {
	w LevelWriter
}

func main() {
	var buf bytes.Buffer
	l := Logger{w: Adapter{W{Out: &buf}}}
	n, _ := l.w.WriteLevel(1, []byte("x"))
	fmt.Println(n)
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "2\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
