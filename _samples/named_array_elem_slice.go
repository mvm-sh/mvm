package main

// Slicing an array whose element is a method-bearing named struct must keep the
// named element identity. The synth array rtype's cached Slice field pointed at
// the methodless layout shadow, so arr[:] yielded []struct{...} not []Props and
// reflect.Set into a []Props slot panicked. (x/text/unicode/norm rb.rune[:] bug.)

import (
	"fmt"
	"reflect"
)

type qc uint8

type Props struct {
	pos   uint8
	flags qc
	index uint16
}

func (p Props) Bound() bool { return p.flags&0x10 == 0 }

type buf struct {
	rune [4]Props
}

func main() {
	var b buf
	s := b.rune[:]
	var want []Props
	fmt.Println(reflect.TypeOf(s), reflect.TypeOf(s) == reflect.TypeOf(want))
}

// Output:
// []main.Props true
