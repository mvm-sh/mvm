package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// An interface embedding a forward-declared interface (cross-file: band.go's
// Banded embeds matrix.go's Matrix in gonum/mat) copied an empty placeholder
// method set, so calls through the embedder picked a random same-named
// concrete method ("mismatched types complex128 and float64"). The decl now
// defers until the embedded interface is parsed.
func TestIfaceEmbedForwardRef(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/mat",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/mat\n",
			// band.go sorts before matrix.go: Banded parses while Matrix is
			// still a placeholder.
			"band.go": `package mat

type Banded interface {
	Matrix
	Bandwidth() int
}

type Band struct{}

func (Band) At(i, j int) float64 { return float64(i + j) }
func (Band) Bandwidth() int      { return 1 }
`,
			"matrix.go": `package mat

type Matrix interface {
	At(i, j int) float64
}

func AtSum(b Banded) float64 { return b.At(1, 2) + b.At(2, 3) }
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/mat"
)

func main() {
	fmt.Println(mat.AtSum(mat.Band{}))
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "8\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
