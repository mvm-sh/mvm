package main

import (
	"fmt"
	"io"
	"strings"
)

// A closure with a package-qualified named return type (e.g. io.Reader) used as
// an element of a func-typed slice composite literal must be recognized as an
// anonymous func, not mis-parsed as a method. The method-vs-anon disambiguation
// keyed on "is the return ident a known type", so a qualified return (where the
// leading ident is a package) fell through to method handling: func(r io.Reader)
// io.Reader{...} was registered as method `int.io`, leaving the slice a zero
// value (len(rs) then panicked "reflect.Value.Len on zero Value"). Now keyed on
// whether the token after the name is a ParenBlock (a method's param list).

func main() {
	rs := []func(io.Reader) io.Reader{
		func(r io.Reader) io.Reader { return r },
	}
	out := rs[0](strings.NewReader("hello"))
	b, _ := io.ReadAll(out)
	fmt.Println(len(rs), string(b))
}

// Output:
// 1 hello
