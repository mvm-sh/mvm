package main

// Two captured-param cell-promotion fixes (rs/zerolog consoleDefaultFormatTimestamp).
//
// capPtr: a captured pointer param sourced from a settable struct field, then
// reassigned. Promoting its slot to a heap cell stores a cell pointer; that must
// be a raw slot overwrite, not a reflect.Set into the old typed value.
//
// condPromote: a captured param reassigned only conditionally. It must be boxed
// into a cell at the prologue (unconditionally), else the skipped branch leaves a
// plain value where the closure capture expects a cell pointer.

type T struct{ x int }
type Box struct{ P *T }

func capPtr(p *T) func() *T {
	if p == nil {
		p = &T{x: 7}
	}
	return func() *T { return p }
}

func condPromote(s string) func() string {
	if s == "" {
		s = "default"
	}
	return func() string { return s }
}

func main() {
	var b Box
	println(capPtr(b.P)().x)
	println(condPromote("")())     // branch taken -> promoted in body
	println(condPromote("kept")()) // branch skipped -> must still be a cell
}

// Output:
// 7
// default
// kept
