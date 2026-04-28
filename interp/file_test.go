package interp

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetPkgfs("../_samples/pkg")

	_, err = i.Eval(p, string(buf))
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
	i := NewInterpreter(golang.GoSpec)
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
	i := NewInterpreter(golang.GoSpec)
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
