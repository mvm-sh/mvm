package main

// A method promoted from an embedded interface, called through an interface,
// where the embedded field holds a native (gob-decoded) value and returns an
// interface the concrete cannot present natively (Big exceeds synth's 16-method
// cap). The promotion must dispatch through the interpreter, not a synth stub.

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
)

type Big interface {
	M0() int
	M1() int
	M2() int
	M3() int
	M4() int
	M5() int
	M6() int
	M7() int
	M8() int
	M9() int
	M10() int
	M11() int
	M12() int
	M13() int
	M14() int
	M15() int
	M16() int
	M17() int
}

type big struct{ V int }

func (b big) M0() int  { return b.V }
func (b big) M1() int  { return 1 }
func (b big) M2() int  { return 2 }
func (b big) M3() int  { return 3 }
func (b big) M4() int  { return 4 }
func (b big) M5() int  { return 5 }
func (b big) M6() int  { return 6 }
func (b big) M7() int  { return 7 }
func (b big) M8() int  { return 8 }
func (b big) M9() int  { return 9 }
func (b big) M10() int { return 10 }
func (b big) M11() int { return 11 }
func (b big) M12() int { return 12 }
func (b big) M13() int { return 13 }
func (b big) M14() int { return 14 }
func (b big) M15() int { return 15 }
func (b big) M16() int { return 16 }
func (b big) M17() int { return 17 }

type Registry interface {
	Lookup() Big
}

type registry struct{ N int }

func (r registry) Lookup() Big { return big{V: r.N} }

// Wrap embeds Registry, so Lookup is a promoted method.
type Wrap struct {
	Registry
}

// Outer is satisfied by Wrap via the promoted Lookup, so a call through Outer
// goes through the embedded-interface promotion path.
type Outer interface {
	Lookup() Big
}

func main() {
	gob.Register(registry{})
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&Wrap{Registry: registry{N: 42}}); err != nil {
		log.Fatal("encode:", err)
	}
	var w Wrap
	if err := gob.NewDecoder(&buf).Decode(&w); err != nil {
		log.Fatal("decode:", err)
	}
	var o Outer = w
	b := o.Lookup()
	fmt.Println(b.M0(), b.M17())
}

// Output:
// 42 17
