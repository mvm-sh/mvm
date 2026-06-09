package main

// A pointer-shaped (direct-iface) struct embedding a native type. *T promotes the
// method correctly; boxing the struct BY VALUE must fail recoverably, never crash
// (the synth stub gets the data word, not a pointer, so value dispatch would fault).

import (
	"bytes"
	"fmt"
	"io"
)

type W struct{ *bytes.Buffer } // single pointer-shaped field => direct-iface

func tryValue() (recovered bool) {
	defer func() {
		if recover() != nil {
			recovered = true
		}
	}()
	w := W{Buffer: bytes.NewBufferString("seed")}
	var iw io.Writer = w
	_, _ = iw.Write([]byte("x")) // a fatal SIGSEGV here could not be recovered
	return false
}

func main() {
	pw := &W{Buffer: &bytes.Buffer{}}
	var iw io.Writer = pw // *W promotes Write correctly
	fmt.Fprint(iw, "ptr")
	fmt.Println(pw.String())
	tryValue()
	fmt.Println("survived")
}

// Output:
// ptr
// survived
