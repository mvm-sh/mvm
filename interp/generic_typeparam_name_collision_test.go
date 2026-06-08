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

// Regression for `mvm test github.com/samber/lo` -> "undefined: Must2".
//
// When a package is loaded as a test target, importingPkg is set to its path,
// so symGet prefers a canonical "<pkg>.<name>" symbol over a bare binding. A
// generic func's type parameter is installed only at its bare name while the
// signature is parsed, so a type param colliding with a package-level symbol of
// the same name (lo's `Must2[T1, T2 any]` next to a package func `T2`) resolved
// to that symbol: the signature parse failed with "T2 is not a type", leaving
// the template's Type nil, and any call reported "undefined: Must2". Fixed by
// installing the placeholders at the pkg-qualified keys too
// (bindTypeParamPlaceholders), mirroring bindTypeParams at instantiation.
func TestGenericTypeParamNameCollidesWithPackageSymbol(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/coll",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/coll\n",
			// Package-level func T2 collides with Must2's type parameter T2.
			"a.go": `package coll

func T2[A, B any](a A, b B) any { return a }

func Must2[T1, T2 any](v1 T1, v2 T2, err any) (T1, T2) {
	if err != nil {
		panic(err)
	}
	return v1, v2
}
`,
			// A separate file calls Must2, forcing inference + registration.
			"use.go": `package coll

func Use() (int, string) {
	return Must2(42, "hello", nil)
}
`,
		},
	})

	var stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.SetIncludeTests(true)

	// Direct-target load (mirrors test_cmd's `i.Eval(target, "")`), which sets
	// importingPkg = "example.com/x/coll".
	if _, err := i.Eval("example.com/x/coll", ""); err != nil {
		t.Fatalf("loading target: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "undefined") {
		t.Errorf("unexpected undefined error: %s", stderr.String())
	}
}

// Companion to the above for the constraint-parsing path: a type param named in
// a composite constraint (E in `[S ~[]E, E int]`) colliding with a package-level
// symbol of the same name. parseTypeParamList installed its placeholders at the
// bare key only, so `E` in `~[]E` resolved to the package func `E`, the
// signature parse failed, and the generic was reported undefined at its callsite.
// Fixed by routing parseTypeParamList through bindTypeParamPlaceholders too.
func TestGenericConstraintTypeParamNameCollidesWithPackageSymbol(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/coll2",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/coll2\n",
			// Package-level func E collides with the type param E used in ~[]E.
			"a.go": `package coll2

func E() int { return 0 }

func Min[S ~[]E, E int](s S) E { return s[0] }
`,
			"use.go": `package coll2

func Use() int { return Min([]int{3, 1, 2}) }
`,
		},
	})

	var stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.SetIncludeTests(true)

	if _, err := i.Eval("example.com/x/coll2", ""); err != nil {
		t.Fatalf("loading target: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "undefined") {
		t.Errorf("unexpected undefined error: %s", stderr.String())
	}
}
