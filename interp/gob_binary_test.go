package interp

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// An interpreted type with unexported fields round-trips through native
// encoding/gob solely via its MarshalBinary/UnmarshalBinary methods. Without
// the gobx arg proxies, the interpreted value reaches (*gob.Encoder).Encode as
// a synthetic struct whose MarshalBinary is invisible, and gob falls back to
// field reflection -> "type struct { PVector_1 int } has no exported fields".
// The proxies wrap it as encoding.BinaryMarshaler/BinaryUnmarshaler dispatching
// back into the interpreter.
func TestGobBinaryMarshaler(t *testing.T) {
	src := `package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

type Vector struct {
	x, y, z int
}

func (v Vector) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintln(&b, v.x, v.y, v.z)
	return b.Bytes(), nil
}

func (v *Vector) UnmarshalBinary(data []byte) error {
	b := bytes.NewBuffer(data)
	_, err := fmt.Fscanln(b, &v.x, &v.y, &v.z)
	return err
}

func main() {
	var network bytes.Buffer
	enc := gob.NewEncoder(&network)
	if err := enc.Encode(Vector{3, 4, 5}); err != nil {
		fmt.Println("encode error:", err)
		return
	}
	dec := gob.NewDecoder(&network)
	var v Vector
	if err := dec.Decode(&v); err != nil {
		fmt.Println("decode error:", err)
		return
	}
	fmt.Println(v)
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "panic") {
		t.Fatalf("got panic: %s", stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "{3 4 5}") {
		t.Errorf("gob BinaryMarshaler round-trip failed: stdout=%q stderr=%q", got, stderr.String())
	}
}

// An interpreted concrete type encoded through a registered gob interface comes
// back on decode as a native value of the type's synthetic reflect.StructOf
// rtype (no native methods). IfaceCall must re-wrap it as an mvm Iface via
// typeByRtype so the compiled value-receiver method dispatches; before the fix
// the call target resolved to nilFuncAddr and panicked with a nil deref.
func TestGobInterfaceRewrap(t *testing.T) {
	// Use a type name distinct from _samples/gob_interface.go's Point: gob's
	// registry is process-global, and each Eval's interpreted type is a distinct
	// synth rtype, so two tests registering the same name in one test binary
	// collide ("duplicate types for main.Point").
	src := `package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"math"
)

type RewrapPoint struct {
	X, Y int
}

func (p RewrapPoint) Hypotenuse() float64 {
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
	gob.Register(RewrapPoint{})
	enc := gob.NewEncoder(&network)
	for i := 1; i <= 3; i++ {
		interfaceEncode(enc, RewrapPoint{3 * i, 4 * i})
	}
	dec := gob.NewDecoder(&network)
	for i := 1; i <= 3; i++ {
		result := interfaceDecode(dec)
		fmt.Println(result.Hypotenuse())
	}
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "panic") {
		t.Fatalf("got panic: %s", stderr.String())
	}
	if got, want := stdout.String(), "5\n10\n15\n"; got != want {
		t.Errorf("gob interface round-trip: got %q want %q (stderr=%q)", got, want, stderr.String())
	}
}
