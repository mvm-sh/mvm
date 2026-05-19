package main

import (
	"io/fs"
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/stdlib/stdmod"
)

// TestRewriteTestFlags checks the go-test -> testing.Main flag-name rewrite.
func TestRewriteTestFlags(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{nil, []string{}},
		{[]string{"-v"}, []string{"-test.v"}},
		{[]string{"-run", "TestFoo"}, []string{"-test.run", "TestFoo"}},
		{[]string{"-count=3"}, []string{"-test.count=3"}},
		{[]string{"--v"}, []string{"--test.v"}},
		{[]string{"-v", "-run", "TestFoo", "-short"}, []string{"-test.v", "-test.run", "TestFoo", "-test.short"}},
		{[]string{"-", "--"}, []string{"-", "--"}},
	}
	for _, c := range cases {
		if got := rewriteTestFlags(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("rewriteTestFlags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSplitTestArgs checks the mvm-flags / target / test-flags partition.
func TestSplitTestArgs(t *testing.T) {
	cases := []struct {
		in       []string
		mvm      []string
		target   string
		testArgs []string
	}{
		{nil, nil, ".", nil},
		{[]string{"-v"}, []string{}, ".", []string{"-v"}},
		{[]string{"./pkg", "-v", "-run", "X"}, []string{}, "./pkg", []string{"-v", "-run", "X"}},
		{[]string{"-x", "./pkg", "-v"}, []string{"-x"}, "./pkg", []string{"-v"}},
		{[]string{"-x=line", "-run", "X"}, []string{"-x=line"}, ".", []string{"-run", "X"}},
		{[]string{"github.com/google/uuid"}, []string{}, "github.com/google/uuid", []string{}},
		{[]string{"-stat", "-v"}, []string{"-stat"}, ".", []string{"-v"}},
		{[]string{"-x", "-stat", "./pkg", "-run", "X"}, []string{"-x", "-stat"}, "./pkg", []string{"-run", "X"}},
	}
	for _, c := range cases {
		mvm, target, testArgs := splitTestArgs(c.in)
		if !reflect.DeepEqual(mvm, c.mvm) || target != c.target || !reflect.DeepEqual(testArgs, c.testArgs) {
			t.Errorf("splitTestArgs(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.in, mvm, target, testArgs, c.mvm, c.target, c.testArgs)
		}
	}
}

// TestBuildModFS exercises the GOPROXY parsing in buildModFS. The shape
// of the resulting modfs (offline vs network-backed) is internal; this
// test only asserts construction never fails or returns nil.
func TestBuildModFS(t *testing.T) {
	cases := []string{
		"",                                    // default proxy
		"off",                                 // explicit disable -> offline
		"direct",                              // VCS-only -> offline
		"https://example.com/proxy",           // single URL
		"https://example.com/proxy,direct",    // first wins
		"off,https://example.com/proxy",       // first wins (offline)
		" https://example.com/proxy , direct", // whitespace tolerated
	}
	for _, goproxy := range cases {
		t.Setenv("GOPROXY", goproxy)
		if got := buildModFS(); got == nil {
			t.Errorf("GOPROXY=%q: buildModFS returned nil", goproxy)
		}
	}
}

// TestEmbeddedStdResolves checks that stdlib imports resolve through
// the default stdlib redirect FS, by virtue of the embedded std zip
// injected at startup. This is the path NewInterpreter installs for
// callers that don't go through wireFS (tests, embed users).
func TestEmbeddedStdResolves(t *testing.T) {
	stdlibFS := stdmod.DefaultFS()

	if _, err := fs.Stat(stdlibFS, "slices"); err != nil {
		t.Fatalf("stat slices: %v", err)
	}
	data, err := fs.ReadFile(stdlibFS, "slices/slices.go")
	if err != nil {
		t.Fatalf("read slices/slices.go: %v", err)
	}
	if len(data) == 0 {
		t.Error("slices/slices.go empty")
	}
}
