package main

// Round-tripping an interpreted struct through a native interface via
// encoding/gob. On decode, gob writes a native struct of Point's synthetic
// reflect.StructOf rtype into the Pythagoras interface; IfaceCall re-wraps that
// native value as an mvm Iface (via typeByRtype) so result.Hypotenuse()
// dispatches through Point's compiled method instead of nil-derefing.
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

// Output:
// 5
// 10
// 15
