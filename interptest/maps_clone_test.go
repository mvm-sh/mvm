package interptest

import "testing"

// TestSynthMapsClone: maps.Clone delegates to a //go:linkname'd clone() filled by
// stdlib/maps_shim.go's source overlay. Native bridges maps; wasm interprets it
// from the mirror + overlay (this runs under the wasm CI).
func TestSynthMapsClone(t *testing.T) {
	const src = `package main
import ("fmt"; "maps"; "sort")
type Counts map[string]int
func main() {
	m := map[string]int{"a": 1, "b": 2}
	c := maps.Clone(m)
	c["z"] = 9
	var n Counts = Counts{"x": 1}
	nc := maps.Clone(n)
	ks := []string{}
	for k := range c { ks = append(ks, k) }
	sort.Strings(ks)
	var nilm map[int]int
	fmt.Printf("clone=%v origHasZ=%v named=%T nilClone=%v\n",
		ks, m["z"] == 9, nc, maps.Clone(nilm) == nil)
}`
	want := "clone=[a b z] origHasZ=false named=main.Counts nilClone=true\n"
	if got := evalOut(t, "maps_clone.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestForwardDeclFilled guards the forward-decl mechanism: a bodyless func
// declaration (asm/linkname stub) is filled by a later body in the same unit
// instead of being rejected as a redeclaration.
func TestForwardDeclFilled(t *testing.T) {
	const src = `package main
import "fmt"
func twice(n int) int
func twice(n int) int { return n * 2 }
func main() { fmt.Print(twice(21)) }
`
	if got := evalOut(t, "fwd.go", src); got != "42" {
		t.Errorf("got %q, want %q", got, "42")
	}
}
