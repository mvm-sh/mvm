package main

import "fmt"

// A function-local named type renders <pkg>.<Name> under %T / %#v just like a
// package-level type, even when it shadows one of a different shape: both "A"s
// here render "main.A", so distinct rtypes may share a qualified name.

type A struct{ a, b, c int }

func main() {
	top := A{1, 2, 3}
	fmt.Printf("%#v\n", top)

	type A struct{ x int }
	local := A{7}
	fmt.Printf("%#v\n", local)
	var p *A = nil
	fmt.Printf("%T\n", p)
}

// Output:
// main.A{a:1, b:2, c:3}
// main.A{x:7}
// *main.A
