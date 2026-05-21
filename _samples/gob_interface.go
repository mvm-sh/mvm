package main

// Round-tripping an interpreted struct through a native interface via
// encoding/gob loses the mvm type identity on decode. gob.Decode writes a
// native struct of Point's synthetic reflect.StructOf type (printed as
// "struct { PPoint_1 int }") into the Pythagoras interface. mvm does not map
// that native rtype back to the interpreted Point *Type, so the call on
// result.Hypotenuse() finds no compiled method (the StructOf placeholder
// carries no methods), resolves the call target to nilFuncAddr, and panics
// with a nil-pointer-deref.
//
// A sibling encode-side gap: encoding a value held in a *local* interface var
// (var p Pythagoras = Point{}) fails with "gob: type not registered for
// interface: vm.Iface", while encoding through a func parameter works.
import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"math"
)

type Point struct {
	X, Y int
}

func (p Point) Hypotenuse() float64 {
	return math.Hypot(float64(p.X), float64(p.Y))
}

type Pythagoras interface {
	Hypotenuse() float64
}

func interfaceEncode(enc *gob.Encoder, p Pythagoras) {
	if err := enc.Encode(&p); err != nil {
		log.Fatal("encode:", err)
	}
}

func interfaceDecode(dec *gob.Decoder) Pythagoras {
	var p Pythagoras
	if err := dec.Decode(&p); err != nil {
		log.Fatal("decode:", err)
	}
	return p
}

func main() {
	var network bytes.Buffer
	gob.Register(Point{})

	enc := gob.NewEncoder(&network)
	for i := 1; i <= 3; i++ {
		interfaceEncode(enc, Point{3 * i, 4 * i})
	}

	dec := gob.NewDecoder(&network)
	for i := 1; i <= 3; i++ {
		result := interfaceDecode(dec)
		fmt.Println(result.Hypotenuse())
	}
}

// skip: native value of an interpreted type (gob-decoded) is not re-wrapped as an mvm Iface, so method dispatch nil-derefs.
// Output:
// 5
// 10
// 15
