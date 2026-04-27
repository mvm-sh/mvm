package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScanImports(t *testing.T) {
	src := `package foo

import "fmt"
import "os"

import (
	"io"
	"strings"

	"example.com/bar"
)
`
	got := scanImports(src)
	want := []string{"fmt", "os", "io", "strings", "example.com/bar"}
	if !slices.Equal(got, want) {
		t.Errorf("scanImports:\n got %v\nwant %v", got, want)
	}
}

func TestExtractQuoted(t *testing.T) {
	tests := []struct {
		line, want string
	}{
		{`"fmt"`, "fmt"},
		{`  "io"  `, "io"},
		{`f "fmt"`, "fmt"},
		{`. "fmt"`, "fmt"},
		{`no quotes`, ""},
		{`"unclosed`, ""},
	}
	for _, tt := range tests {
		if got := extractQuoted(tt.line); got != tt.want {
			t.Errorf("extractQuoted(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestExtractImports(t *testing.T) {
	dir := filepath.Join("..", "..", "vm")
	imports, err := extractImports(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"fmt", "reflect", "math"} {
		if !slices.Contains(imports, pkg) {
			t.Errorf("missing import %q in %v", pkg, imports)
		}
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		dir    string
		consts []string
		types  []string
		funcs  []string
	}{
		{
			dir:    filepath.Join("..", "..", "vm"),
			consts: []string{"Nop", "Addr", "Call", "Return", "Global", "Local"},
			types:  []string{"Op", "Machine", "Value", "Type", "Instruction"},
			funcs:  []string{"NewMachine", "ValueOf", "TypeOf"},
		},
		{
			dir:    filepath.Join("..", "..", "symbol"),
			consts: []string{"Const", "Func", "Type", "Var", "UnsetAddr"},
			types:  []string{"Symbol", "Kind", "SymMap", "Package"},
		},
		{
			dir:   filepath.Join("testdata", "bodyless"),
			types: []string{"Duration"},
			funcs: []string{"Sleep", "Now"},
		},
	}

	for _, tt := range tests {
		t.Run(filepath.Base(tt.dir), func(t *testing.T) {
			output := captureStdout(t, func() {
				if err := run(os.Stdout, tt.dir); err != nil {
					t.Fatalf("run(%q): %v", tt.dir, err)
				}
			})

			lines := map[string]bool{}
			for _, line := range strings.Split(output, "\n") {
				if line != "" {
					lines[line] = true
				}
			}

			for _, name := range tt.consts {
				if !lines["const "+name] {
					t.Errorf("missing: const %s", name)
				}
			}
			for _, name := range tt.types {
				if !lines["type "+name] {
					t.Errorf("missing: type %s", name)
				}
			}
			for _, name := range tt.funcs {
				if !lines["func "+name] {
					t.Errorf("missing: func %s", name)
				}
			}
		})
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w

	fn()

	os.Stdout = old
	_ = w.Close()

	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
