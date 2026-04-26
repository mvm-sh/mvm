package interp_test

import (
	"fmt"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/symbol"
)

func newAutoImportInterp(t *testing.T) *interp.Interp {
	t.Helper()
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.AutoImportPackages()
	return i
}

func TestAutoImportBasic(t *testing.T) {
	tests := []etest{
		{n: "fmt", src: `fmt.Sprint(1+2)`, res: "3"},
		{n: "strings", src: `len(strings.Split("a,b,c", ","))`, res: "3"},
		{n: "strconv", src: `strconv.Itoa(42)`, res: "42"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.n, func(t *testing.T) {
			t.Parallel()
			i := newAutoImportInterp(t)
			r, err := i.Eval("test", tc.src)
			if err != nil {
				t.Fatalf("eval %q: %v", tc.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != tc.res {
				t.Errorf("got %q, want %q", got, tc.res)
			}
		})
	}
}

func TestAutoImportCollisionResolution(t *testing.T) {
	i := newAutoImportInterp(t)
	cases := []struct{ name, wantPkgPath string }{
		{"rand", "math/rand"},
		{"template", "text/template"},
		{"scanner", "text/scanner"},
		{"pprof", "runtime/pprof"},
	}
	for _, c := range cases {
		sym, ok := i.Symbols[c.name]
		if !ok {
			t.Errorf("symbol %q not registered", c.name)
			continue
		}
		if sym.PkgPath != c.wantPkgPath {
			t.Errorf("%s: got PkgPath %q, want %q", c.name, sym.PkgPath, c.wantPkgPath)
		}
	}
}

func TestAutoImportExplicitOverride(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("pre", `import "html/template"`); err != nil {
		t.Fatalf("explicit import failed: %v", err)
	}
	sym, ok := i.Symbols["template"]
	if !ok {
		t.Fatal("symbol template missing after explicit import")
	}
	if sym.PkgPath != "html/template" {
		t.Errorf("explicit import did not override: got PkgPath %q", sym.PkgPath)
	}
}

func TestAutoImportCollisionFallback(t *testing.T) {
	// Collision not covered by the preference table: fewest-segments wins,
	// ties broken lexically.
	i := interp.NewInterpreter(golang.GoSpec)
	i.Packages["a/widget"] = symbol.BinPkg(nil, "a/widget")
	i.Packages["b/c/widget"] = symbol.BinPkg(nil, "b/c/widget")
	i.Packages["a/b/widget"] = symbol.BinPkg(nil, "a/b/widget")
	i.AutoImportPackages()
	sym, ok := i.Symbols["widget"]
	if !ok {
		t.Fatal("widget symbol not registered")
	}
	if sym.PkgPath != "a/widget" {
		t.Errorf("fallback picked %q, want %q", sym.PkgPath, "a/widget")
	}
}

func TestAutoImportDisabledByDefault(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	if _, err := i.Eval("test", `time.Now()`); err == nil {
		t.Fatal("expected time.Now() to fail without AutoImportPackages, got nil error")
	}
}
