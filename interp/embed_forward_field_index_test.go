package interp

import "testing"

// A struct embeds a named slice type whose own element type is forward
// referenced (defined later in the unit). The embedded field's type is cloned
// at parse time while still an empty placeholder; its Base is only materialized
// afterwards. Indexing the embedded field in a method body used to read the
// thin clone (Kind=Invalid) and nil-deref in Type.Elem. resolveFieldByPath now
// adopts the field type's materialized Base. Shape mirrors gonum kdtree's
// nbPlane{Dim; nbPoints} with `type nbPoint Point` defined across files.
func TestEmbeddedForwardFieldIndex(t *testing.T) {
	src := `package main

import "fmt"

type Dim int

type Comparable interface {
	Compare(Comparable, Dim) float64
}

var _ Comparable = nbPoint{}

type nbPoint Point

func (p nbPoint) Compare(c Comparable, d Dim) float64 { q := c.(nbPoint); return p[d] - q[d] }

type nbPoints []nbPoint

type nbPlane struct {
	Dim
	nbPoints
}

func (p nbPlane) Less(i, j int) bool { return p.nbPoints[i][p.Dim] < p.nbPoints[j][p.Dim] }

type Point []float64

func main() {
	pl := nbPlane{Dim: 0, nbPoints: nbPoints{{1, 2}, {3, 4}}}
	fmt.Println(pl.Less(0, 1))
}
`
	if got := runMain(t, "embedfwd", src); got != "true\n" {
		t.Errorf("got %q, want %q", got, "true\n")
	}
}
