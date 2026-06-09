package main

// A struct satisfying a native interface via a method promoted from an embedded
// native type (Write from *bytes.Buffer) must be assignable to it as a field, a
// func arg, and a var. reflect.StructOf makes no promotion wrappers, so the synth
// rtype gets forwarding methods attached. (rs/zerolog LevelWriterAdapter)

import (
	"bytes"
	"fmt"
	"io"
)

type closableBuffer struct {
	*bytes.Buffer
	closed bool
}

func (cb *closableBuffer) Close() error { cb.closed = true; return nil }

type Adapter struct{ io.Writer }

func sink(w io.Writer) { fmt.Fprint(w, "B") }

func main() {
	cb := &closableBuffer{Buffer: &bytes.Buffer{}}
	a := Adapter{Writer: cb} // struct field
	fmt.Fprint(a, "A")
	sink(cb)              // native func arg
	var w io.Writer = cb  // var
	fmt.Fprint(w, "C")
	// the value still satisfies io.Closer via its own method
	_ = cb.Close()
	fmt.Println(cb.String(), cb.closed)
}

// Output:
// ABC true
