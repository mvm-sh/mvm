package main

import (
	"reflect"

	a "example.com/samenamea"
	b "example.com/samenameb"
)

// (*pkg.T)(nil) must resolve per package even when two packages share a simple type name.
var goTypes = []any{(*a.Message)(nil), (*b.Message)(nil)}

func main() {
	for _, t := range goTypes {
		println(reflect.TypeOf(t).String())
	}
	println(reflect.TypeOf((*a.Message)(nil)) == reflect.TypeOf((*b.Message)(nil)))
}

// Output:
// *samenamea.Message
// *samenameb.Message
// false
