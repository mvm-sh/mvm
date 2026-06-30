// &local of an interface-typed local must yield a pointer of the declared
// static type (*error, not *interface{}) into heap storage, so it is both
// assignable to a *error and stable after the frame's slots are reused.
package main

import (
	"errors"
	"fmt"
)

type myErr struct{ s string }

func (e *myErr) Error() string { return "my:" + e.s }

var gErr *error
var gAny *any

func storeErr() {
	err := errors.New("kept") // native error in an error local
	gErr = &err               // must be *error, must outlive the frame
}

func storeInterp() {
	var err error = &myErr{"interp"} // interpreted concrete in an error local
	gErr = &err
}

func storeAny() {
	var v any = 42
	gAny = &v
}

func churn() { // reuse the frame slots storeErr/storeAny used
	a := errors.New("AAAA")
	b := errors.New("BBBB")
	_ = a
	_ = b
}

func writeback() {
	err := errors.New("first")
	p := &err
	*p = errors.New("second") // through the pointer
	fmt.Println("local sees:", err.Error())
	err = &myErr{"third"} // through the local
	fmt.Println("pointer sees:", (*p).Error())
}

func main() {
	storeErr()
	churn()
	fmt.Println("native:", (*gErr).Error())

	storeInterp()
	churn()
	fmt.Println("interp:", (*gErr).Error())

	storeAny()
	churn()
	fmt.Println("any:", *gAny)

	writeback()
}

// Output:
// native: kept
// interp: my:interp
// any: 42
// local sees: second
// pointer sees: my:third
