package goparser

import (
	"slices"
	"testing"
	"testing/fstest"

	"github.com/mvm-sh/mvm/lang/golang"
)

func TestExtractImports(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "single and grouped",
			src: `package p
import "fmt"
import (
	"go/scanner"
	alias "go/token"
	_ "strings"
)`,
			want: []string{"fmt", "go/scanner", "go/token", "strings"},
		},
		{
			name: "embedded source in raw string is ignored",
			src: `package p
import (
	"fmt"
	"testing"
)
const bsrc = ` + "`" + `
package b
import (
	"a"
	"html/template"
)
` + "`" + `
`,
			want: []string{"fmt", "testing"},
		},
		{
			name: "raw-string single import line ignored",
			src: `package p
import "go/types"
var src = ` + "`" + `import "lib"` + "`" + `
`,
			want: []string{"go/types"},
		},
		{
			name: "import keyword in comments ignored",
			src: `package p
// import "commented/out"
import "regexp" /* import "also/not" */
`,
			want: []string{"regexp"},
		},
		{
			name: "quoted token in block comment not harvested",
			src: `package p
var s = ` + "`" + `import  /* ERROR "8:9" */  // blanks` + "`" + `
import "strings"
`,
			want: []string{"strings"},
		},
	}
	p := NewParser(golang.GoSpec, false)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.extractImports(tc.src)
			if !slices.Equal(got, tc.want) {
				t.Errorf("extractImports() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCollectPackageSourcesTestsLast guards that non-test files are presented
// before _test.go files (the order the go tool uses), so a package file's init
// runs before an in-package _test.go init that depends on it. ReadDir is lexical
// and would otherwise interleave them -- the failure that left modernc.org/sqlite's
// vtab.RegisterModule hook unset when module_volatile_test.go's init ran.
func TestCollectPackageSourcesTestsLast(t *testing.T) {
	fsys := fstest.MapFS{
		"zzz.go":      {Data: []byte("package p\n")},
		"aaa.go":      {Data: []byte("package p\n")},
		"mmm_test.go": {Data: []byte("package p\nimport \"testing\"\nfunc TestM(t *testing.T) {}\n")},
		"bbb_test.go": {Data: []byte("package p\nimport \"testing\"\nfunc TestB(t *testing.T) {}\n")},
	}
	p := NewParser(golang.GoSpec, false)
	srcs, err := p.collectPackageSources(fsys, ".", "", true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range srcs {
		got = append(got, s.Name)
	}
	want := []string{"aaa.go", "zzz.go", "bbb_test.go", "mmm_test.go"}
	if !slices.Equal(got, want) {
		t.Errorf("source order = %v, want %v (non-test files must precede _test.go)", got, want)
	}
}
