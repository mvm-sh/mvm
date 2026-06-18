package main

import (
	"errors"
	"fmt"
)

// A select recv-assign to a captured variable (`select { case x = <-ch: }`)
// must write through the variable's heap cell, like a plain `x = <-ch` does.
// The variable is cell-promoted because a closure captures it; storing the
// received value raw into the slot (instead of through the cell) leaves a
// later read -- which dereferences the cell -- looking at a non-cell value.
// Reduced from grpc internal/testutils (`case err = <-dialErr: errors.Is(...)`).
func recvAssign() error {
	var err error
	done := make(chan bool, 1)
	go func() { _ = err; done <- true }() // capture err -> err becomes a heap cell
	<-done
	ch := make(chan error, 1)
	ch <- errors.New("boom")
	select {
	case err = <-ch:
	}
	return err // read through the cell; must be "boom", not a panic
}

func main() {
	err := recvAssign()
	fmt.Println(err, errors.Is(err, err))
}

// Output:
// boom true
