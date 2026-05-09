// Regression test: closure-in-struct dispatch under fmt.Sprintf("%+v")
// on a recursively wrapped error chain. Mirrors pkg/errors' TestFormatGeneric
// shape so the bridged Format path stays exercised.
package main

import (
	"fmt"
	"io"
)

type errA struct{ s string }

func (e *errA) Error() string                 { return e.s }
func (e *errA) Format(s fmt.State, verb rune) { io.WriteString(s, e.s) }

type errB struct{ inner error }

func (e *errB) Error() string { return e.inner.Error() + "+B" }
func (e *errB) Format(s fmt.State, verb rune) {
	fmt.Fprintf(s, "%+v", e.inner)
	io.WriteString(s, "+B")
}

type errC struct{ inner error }

func (e *errC) Error() string { return e.inner.Error() + "+C" }
func (e *errC) Format(s fmt.State, verb rune) {
	fmt.Fprintf(s, "%+v", e.inner)
	io.WriteString(s, "+C")
}

type errD struct{ inner error }

func (e *errD) Error() string { return e.inner.Error() + "+D" }
func (e *errD) Format(s fmt.State, verb rune) {
	fmt.Fprintf(s, "%+v", e.inner)
	io.WriteString(s, "+D")
}

func MakeB(err error) error { return &errB{err} }
func MakeC(err error) error { return &errC{err} }
func MakeD(err error) error { return &errD{err} }

type wrapper struct {
	fn  func(err error) error
	tag string
}

var failures, calls int

func recur(before error, list []wrapper, depth int) {
	for _, w := range list {
		err := w.fn(before)
		got := fmt.Sprintf("%+v", err)
		var sfx string
		switch w.tag {
		case "B":
			sfx = "+B"
		case "C":
			sfx = "+C"
		case "D":
			sfx = "+D"
		}
		calls++
		if len(got) < 2 || got[len(got)-2:] != sfx {
			failures++
		}
		if depth > 0 {
			recur(err, list, depth-1)
		}
	}
}

func main() {
	list := []wrapper{
		{MakeB, "B"},
		{MakeC, "C"},
		{MakeD, "D"},
	}
	recur(&errA{"a"}, list, 3)
	fmt.Printf("failures=%d calls=%d\n", failures, calls)
}

// Output:
// failures=0 calls=120
