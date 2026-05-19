package main

import "fmt"

type (
	intValue      int
	triStateValue int
)

func (i *intValue) Set(s string) string      { return "intValue:" + s }
func (i *intValue) Type() string             { return "int" }
func (t *triStateValue) Set(s string) string { return "tri:" + s }
func (t *triStateValue) Type() string        { return "tri" }

type valuer interface {
	Set(string) string
	Type() string
}

func use(v valuer) { fmt.Println(v.Type(), v.Set("x")) }

func main() {
	var a intValue
	var b triStateValue
	use(&a)
	use(&b)
}

// Output:
// int intValue:x
// tri tri:x
