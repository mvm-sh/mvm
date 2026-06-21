package interptest

import (
	"bytes"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

func TestFile(t *testing.T) {
	baseDir := filepath.Join("..", "_samples")
	files, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Ext(file.Name()) != ".go" {
			continue
		}
		t.Run(file.Name(), func(t *testing.T) {
			t.Parallel()
			runFile(t, filepath.Join(baseDir, file.Name()))
		})
	}
}

func runFile(t *testing.T, p string) {
	t.Helper()
	buf, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want, isErr, skip := commentData(p, buf)
	if skip {
		t.Skip()
	}

	var stdout, stderr bytes.Buffer

	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	// When MVM_TRACE is set, tee stderr to the real stderr so trace
	// output is visible via `go test -v` instead of vanishing into the buffer.
	traceLine, traceOp := interp.ParseTraceModes(os.Getenv("MVM_TRACE"))
	var errW io.Writer = &stderr
	if traceLine || traceOp {
		errW = io.MultiWriter(&stderr, os.Stderr)
	}
	i.SetIO(os.Stdin, &stdout, errW)
	i.SetPkgfs("../_samples/pkg")

	// Normalize the Eval label so the source registry sees the same path
	// shape that `mvm run` produces (which a user invokes from the repo
	// root). Without this the harness's "../" prefix leaks into pkg names
	// and file paths reported by runtime.Callers/FuncForPC.
	label := filepath.Join("_samples", filepath.Base(p))
	_, err = i.Eval(label, string(buf))
	if isErr {
		if err == nil {
			t.Fatalf("got nil error, want: %q", want)
		}
		if res := strings.TrimSpace(err.Error()); !strings.Contains(res, want) {
			t.Errorf("got: %q, want: %q", res, want)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if res := stdout.String(); res != want {
		t.Errorf("\ngot:  %q,\nwant: %q", res, want)
	}
}

func TestUndefinedVarInitPosition(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	_, err := i.Eval("repro.go", "package main\n\nvar x = undefinedSym\n\nfunc main() { _ = x }\n")
	if err == nil {
		t.Fatal("got nil error, want undefined: undefinedSym")
	}
	msg := err.Error()
	if !strings.Contains(msg, "repro.go:3") {
		t.Errorf("error not positioned at the var decl: %q", msg)
	}
	if strings.Contains(msg, "<shim>") {
		t.Errorf("error misattributed to a shim source: %q", msg)
	}
}

func TestImportDiamond(t *testing.T) {
	// Both pkg2 and pkg3 import pkg1. Verify pkg1 is registered once.
	src := `package main

import (
	"example.com/pkg2"
	"example.com/pkg3"
)

func main() {
	println(pkg2.W, pkg3.H())
}
`
	var stdout bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetPkgfs("../_samples/pkg")

	if _, err := i.Eval("test", src); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "hello world hello!\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// pkg1 must appear exactly once in Packages (not compiled twice).
	count := 0
	for k := range i.Packages {
		if k == "example.com/pkg1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("pkg1 registered %d times in Packages, want 1", count)
	}
}

// A defined type (TI int / TB byte) must not share its underlying's derived
// cache, or its synth cascade flips a later file's map[int]bool / []byte to
// map[TI]bool / []TB. A flipped field rtype makes the assignment below panic.
func TestNamedTypeNoDerivedAliasing(t *testing.T) {
	var stdout bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.SetIO(os.Stdin, &stdout, os.Stderr)

	// Order-sensitive (mirrors evalLocalDir's alphabetical walk): the struct's
	// containers must be built before the named type attaches and cascades.
	state := `package p
type State struct {
	flag map[int]bool
	raw  []byte
}
`
	if _, err := i.Eval("state.go", state); err != nil {
		t.Fatal(err)
	}

	types := `package p
type TI int
func (v TI) String() string { return "ti" }
type TB byte
func (v TB) String() string { return "tb" }
`
	if _, err := i.Eval("types.go", types); err != nil {
		t.Fatal(err)
	}

	run := `package p
func run() {
	s := State{}
	s.flag = make(map[int]bool)
	s.raw = []byte("xy")
	s.flag[1] = true
	println(len(s.flag), len(s.raw), s.flag[1])
}
run()
`
	if _, err := i.Eval("run.go", run); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "1 2 true\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGenericExport(t *testing.T) {
	src := `package main

import "example.com/pkg6"

func main() {
	println(pkg6.Max[int](3, 5))
	println(pkg6.Max[string]("alpha", "beta"))
	println(pkg6.Id(42))
	println(pkg6.Id("hello"))
	b := pkg6.Box[int]{Value: 7}
	println(b.Value)
}
`
	var stdout bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetPkgfs("../_samples/pkg")

	if _, err := i.Eval("test", src); err != nil {
		t.Fatal(err)
	}
	want := "5\nbeta\n42\nhello\n7\n"
	if got := stdout.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func commentData(p string, buf []byte) (text string, isErr, skip bool) {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, p, buf, parser.ParseComments)
	if len(f.Comments) == 0 {
		return
	}
	text = f.Comments[len(f.Comments)-1].Text()
	switch {
	case strings.HasPrefix(text, "skip:"):
		return "", false, true
	case strings.HasPrefix(text, "Error:\n"):
		return strings.TrimPrefix(text, "Error:\n"), true, false
	case strings.HasPrefix(text, "Output:\n"):
		return strings.TrimPrefix(text, "Output:\n"), false, false
	}
	return
}
