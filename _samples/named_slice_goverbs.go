// Regression test: %#v on a slice whose element is a defined type with a
// Format method must qualify the type name and dispatch each element
// through the user Format body.
package main

import (
	"fmt"
	"io"
)

type Frame uintptr

func (f Frame) Format(s fmt.State, verb rune) {
	switch verb {
	case 'v':
		_, _ = fmt.Fprintf(s, "F(%d)", uintptr(f))
	default:
		_, _ = io.WriteString(s, "F?")
	}
}

func main() {
	fmt.Printf("%#v\n", []Frame(nil))
	fmt.Printf("%#v\n", []Frame{})
	fmt.Printf("%#v\n", []Frame{1, 2, 3})
}

// Output:
// []main.Frame(nil)
// []main.Frame{}
// []main.Frame{F(1), F(2), F(3)}
