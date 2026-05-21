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
