package main

// Legal mutual struct cycle broken by a pointer (cf. x/text/unicode/cldr's Elem
// types, which once failed with "undefined: Common"): hidden reaches Common only
// via *struct{ Common }, Common embeds hidden by value.
//
// Only direct fields are read here. Reading a field promoted THROUGH the cycle
// (c.Alias.Type) still recurses: anonymous structs lack a materialize cycle break.

import "fmt"

type hidden struct {
	Alias *struct {
		Common
		Source string
	}
}

type Common struct {
	Type string
	hidden
}

func (c *Common) GetType() string { return c.Type }

func main() {
	c := Common{Type: "root"}
	c.Alias = &struct {
		Common
		Source string
	}{Common{Type: "nested"}, "src"}
	fmt.Println(c.GetType(), c.Alias.Source)
}

// Output:
// root src
