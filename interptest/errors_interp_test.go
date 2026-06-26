package interptest

import "testing"

// errors: mirrored on wasm, bridged on native. errors.Is/As/Unwrap walk the
// Unwrap chain dispatching err.Unwrap/Is/As on the error interface; bridged on
// wasm, an interpreted wrapped error traps ("unreachable method called").
// Mirroring keeps errors interpreted so custom error chains work.
func TestSynthErrors(t *testing.T) {
	const src = `package main
import (
	"errors"
	"fmt"
)
var ErrNotFound = errors.New("not found")
type pathErr struct{ path string; err error }
func (e *pathErr) Error() string { return e.path + ": " + e.err.Error() }
func (e *pathErr) Unwrap() error { return e.err }
func main() {
	e := fmt.Errorf("open %w", ErrNotFound)
	fmt.Println("Is sentinel:", errors.Is(e, ErrNotFound))
	pe := &pathErr{path: "/x", err: ErrNotFound}
	var target *pathErr
	fmt.Println("As:", errors.As(pe, &target), target.path)
	fmt.Println("Is via chain:", errors.Is(pe, ErrNotFound))
	j := errors.Join(errors.New("e1"), errors.New("e2"))
	fmt.Println("Join Is:", errors.Is(j, ErrNotFound))
	fmt.Println("Unwrap:", errors.Unwrap(pe) == ErrNotFound)
}`
	want := "Is sentinel: true\n" +
		"As: true /x\n" +
		"Is via chain: true\n" +
		"Join Is: false\n" +
		"Unwrap: true\n"
	if got := evalOut(t, "errors.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
