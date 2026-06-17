package main

// A struct field of a named func type declared *before* the type, whose
// signature references another forward-declared named func type.
type serverOptions struct {
	streamInt SSI
}

type SSI func(h SH) int

type SH func() int

func main() {
	o := serverOptions{streamInt: func(h SH) int { return h() + 1 }}
	r := o.streamInt(func() int { return 41 })
	println(r)
}

// Output:
// 42
