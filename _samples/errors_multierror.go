package main

// Multi-error types (Unwrap() []error, as produced by errors.Join or
// hashicorp-style trees) are bridged via stdlib.BridgeError*UnwrapMulti, so the
// native errors.Is / errors.As chain walk descends every branch of the tree.
// vm.bridgeMethodName routes the interpreted Unwrap() []error to a distinct
// bridge key so it does not collide with the single-error Unwrap() error.
import (
	"errors"
	"fmt"
	"io/fs"
)

type multiErr []error

func (m multiErr) Error() string   { return "multi" }
func (m multiErr) Unwrap() []error { return []error(m) }

func main() {
	leaf := fmt.Errorf("wrap: %w", fs.ErrPermission)
	var err error = multiErr{errors.New("other"), leaf}
	fmt.Println("is:", errors.Is(err, fs.ErrPermission))
	fmt.Println("is-miss:", errors.Is(err, fs.ErrNotExist))

	pathErr := &fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}
	var err2 error = multiErr{errors.New("other"), pathErr}
	var pe *fs.PathError
	fmt.Println("as:", errors.As(err2, &pe), pe.Path)
}

// Output:
// is: true
// is-miss: false
// as: true /x
