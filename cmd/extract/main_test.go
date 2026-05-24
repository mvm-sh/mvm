package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/symbol"
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

// TestExtractFromInsideDir is a regression for #15.
func TestExtractFromInsideDir(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "bodyless"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	groups, _, _, err := extract(".")
	if err != nil {
		t.Fatalf("extract(%q): %v", ".", err)
	}
	if !slices.Contains(groups[symbol.Type], "Duration") {
		t.Errorf("missing type Duration; got types %v", groups[symbol.Type])
	}
	if !slices.Contains(groups[symbol.Func], "Sleep") {
		t.Errorf("missing func Sleep; got funcs %v", groups[symbol.Func])
	}
}

// mustParse fails the test if src is not valid Go.
func mustParse(t *testing.T, src string) {
	t.Helper()
	if _, err := parser.ParseFile(token.NewFileSet(), "bindings.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, src)
	}
}

func TestBuildValuesFile(t *testing.T) {
	groups := map[symbol.Kind][]string{
		symbol.Const: {"MaxThing", "Big"},
		symbol.Func:  {"Do"},
		symbol.Var:   {"Default"},
		symbol.Type:  {"Thing"},
	}
	typedConsts := map[string]string{"Big": "uint64"} // overflows int -> wrapped
	src, err := buildValuesFile("example.com/foo", "foo", "mypkg", groups, typedConsts)
	if err != nil {
		t.Fatal(err)
	}
	got := string(src)
	mustParse(t, got)

	for _, want := range []string{
		"package mypkg",
		`var fooValues = map[string]map[string]reflect.Value{`,
		`"example.com/foo": {`,
		`reflect.ValueOf(foo.Do)`,            // func by value
		`reflect.ValueOf(foo.MaxThing)`,      // const by value
		`reflect.ValueOf(uint64(foo.Big))`,   // overflowing const, wrapped
		`reflect.ValueOf(&foo.Default)`,      // var by address
		`reflect.ValueOf((*foo.Thing)(nil))`, // type as typed nil pointer
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestBuildValuesFileEmpty checks that a package with no exported symbols still
// produces valid Go and does not import the package itself (which would be an
// unused import).
func TestBuildValuesFileEmpty(t *testing.T) {
	src, err := buildValuesFile("example.com/empty", "empty", "main", map[symbol.Kind][]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(src)
	mustParse(t, got)
	// The import line would be `\t"example.com/empty"` followed by a newline;
	// the outer-map key is `"example.com/empty": {`. Only the former should be
	// absent.
	if strings.Contains(got, "\t\"example.com/empty\"\n") {
		t.Errorf("empty package must not import itself:\n%s", got)
	}
}

// TestRunValues exercises the full default path (go list -> extract -> render)
// against a real in-module package, with no network access.
func TestRunValues(t *testing.T) {
	var buf bytes.Buffer
	if err := runValues(&buf, "github.com/mvm-sh/mvm/symbol", ""); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	mustParse(t, got)

	for _, want := range []string{
		`var symbolValues = map[string]map[string]reflect.Value{`,
		`"github.com/mvm-sh/mvm/symbol": {`,
		`reflect.ValueOf(symbol.BinPkg)`,         // a func
		`reflect.ValueOf((*symbol.Symbol)(nil))`, // a type
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRunValuesRelativePath checks that a relative target (".", "./x") is
// canonicalized to the real import path in the generated import and map key,
// rather than leaking the literal relative string (which produces an
// uncompilable `import "."` and a key no interpreted import can match).
func TestRunValuesRelativePath(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "symbol"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	var buf bytes.Buffer
	if err := runValues(&buf, ".", ""); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	mustParse(t, got)

	if !strings.Contains(got, `"github.com/mvm-sh/mvm/symbol"`) {
		t.Errorf("expected canonical import path; got:\n%s", got)
	}
	if strings.Contains(got, `".":`) || strings.Contains(got, "\t\".\"\n") {
		t.Errorf("literal relative path leaked into output:\n%s", got)
	}
}

// TestPackageNameFromDir covers the dirOverride alias resolution: a trailing
// comment on the package clause must be stripped, and a //go:build ignore
// generator file declaring `package main` must not hijack the name. Fixtures
// are built in a temp dir rather than committed under testdata so they need not
// be tracked (and to sidestep the repo's broad `extract` .gitignore pattern,
// which silently ignores untracked files anywhere under cmd/extract).
func TestPackageNameFromDir(t *testing.T) {
	root := t.TempDir()
	mkpkg := func(name string, files map[string]string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for fn, src := range files {
			if err := os.WriteFile(filepath.Join(dir, fn), []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	comment := mkpkg("pkgcomment", map[string]string{
		"pkgcomment.go": "package foo // trailing comment\n\nfunc Bar() {}\n",
	})
	// aaa_gen.go sorts before zoo.go; build constraints must exclude it.
	ignore := mkpkg("buildignore", map[string]string{
		"aaa_gen.go": "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		"zoo.go":     "package realpkg\n\nfunc Z() {}\n",
	})

	tests := []struct{ name, dir, want string }{
		{"plain", filepath.Join("testdata", "bodyless"), "bodyless"},
		{"trailing comment", comment, "foo"},
		{"build-ignored main", ignore, "realpkg"},
	}
	for _, tt := range tests {
		if got := packageNameFromDir(tt.dir); got != tt.want {
			t.Errorf("%s: packageNameFromDir(%q) = %q, want %q", tt.name, tt.dir, got, tt.want)
		}
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
