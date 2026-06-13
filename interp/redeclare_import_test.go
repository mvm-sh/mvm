package interp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

func TestRedeclareAsImport(t *testing.T) {
	run(t, []etest{
		{n: "var_vs_import", src: `import "sort"; var sort = 1; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "const_vs_import", src: `import "sort"; const sort = 1; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "type_vs_import", src: `import "sort"; type sort = int; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "func_vs_import", src: `import "sort"; func sort() {}; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "grouped_var_vs_import", src: `import "sort"; var ( a = 1; sort = 2 ); func run() int { return a }; run()`, err: "redeclared in this block"},

		// Valid Go: a local name shadows the imported package -- distinct scoped
		// key, must resolve to the local, never trip the check.
		{n: "local_shadow_ok", src: `import "sort"; func run() int { sort := 42; return sort }; run()`, res: "42"},
	})
}

func TestVarNamedLikeOwnPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "selfname"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package selfname

import "sort"

var selfname = sort.SearchInts([]int{1, 2, 3}, 2)

func Value() int { return selfname }
`
	if err := os.WriteFile(filepath.Join(dir, "selfname", "selfname.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetPkgfs(dir)
	r, err := i.Eval("t", `import "selfname"; selfname.Value()`+"\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := r.Interface(); got != 1 {
		t.Fatalf("selfname.Value() = %v, want 1", got)
	}
}

func TestForwardTypeBaseShadowsAutoImport(t *testing.T) {
	for _, name := range []string{"syntax", "bytes", "sort"} {
		t.Run(name, func(t *testing.T) {
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			intp.AutoImportPackages() // ambient-bind the short name as a Pkg symbol
			// Outer's base is declared AFTER Outer, so the base is a forward ref.
			src := "type Outer " + name + "; type " + name + " int8; func run() int { var v Outer = 2; return int(v) }; run()\n"
			r, err := intp.Eval("t", src)
			if err != nil {
				t.Fatalf("forward type base %q: unexpected error %v", name, err)
			}
			if got := r.Interface(); got != 2 {
				t.Fatalf("%s: got %v, want 2", name, got)
			}
		})
	}
}

func TestRedeclareVsAutoImport(t *testing.T) {
	for _, name := range []string{"sort", "bytes", "time"} {
		t.Run(name, func(t *testing.T) {
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			intp.AutoImportPackages() // ambient-bind sort/bytes/time as Pkg symbols
			if _, err := intp.Eval("t", "var "+name+" = 42\n"); err != nil {
				t.Fatalf("var %s = 42 with no explicit import: unexpected error %v", name, err)
			}
			r, err := intp.Eval("t2", name+"\n")
			if err != nil {
				t.Fatalf("read back %s: %v", name, err)
			}
			if got := r.Interface(); got != 42 {
				t.Fatalf("%s = %v, want 42", name, got)
			}
		})
	}
}
