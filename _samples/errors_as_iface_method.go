package main

import (
	"errors"
	"fmt"
)

type status struct{ msg string }

type myErr struct{ m string }

func (e myErr) Error() string   { return e.m }
func (e myErr) Status() *status { return &status{msg: e.m} }

type pkgIface interface{ Status() *status }

type otherErr struct{}

func (otherErr) Error() string   { return "other" }
func (otherErr) Status() *status { return &status{msg: "OTHER"} }

func viaLocal(err error) string {
	type localIface interface{ Status() *status }
	var gs localIface
	if errors.As(err, &gs) {
		return fmt.Sprintf("%T:%s", gs, gs.Status().msg)
	}
	return "no-match"
}

func viaPkg(err error) string {
	var gs pkgIface
	if errors.As(err, &gs) {
		return fmt.Sprintf("%T:%s", gs, gs.Status().msg)
	}
	return "no-match"
}

func main() {
	direct := error(myErr{m: "d"})
	wrapped := fmt.Errorf("w: %w", myErr{m: "w"})
	fmt.Println(viaPkg(direct))
	fmt.Println(viaPkg(wrapped))
	fmt.Println(viaLocal(direct))
	fmt.Println(viaLocal(wrapped))

	// A non-matching errors.As must leave a pre-set non-nil target unchanged
	// (not reinterpret the cell as Go iface layout).
	var preset pkgIface = otherErr{}
	matched := errors.As(errors.New("plain"), &preset)
	fmt.Printf("%v:%s\n", matched, preset.Status().msg)

	// Two matching errors.As into the same target: the second must overwrite,
	// not crash on the first call's normalized cell.
	var reused pkgIface
	errors.As(direct, &reused)
	r1 := reused.Status().msg
	errors.As(error(myErr{m: "x"}), &reused)
	fmt.Printf("%s,%s\n", r1, reused.Status().msg)
}

// Output:
// main.myErr:d
// main.myErr:w
// main.myErr:d
// main.myErr:w
// false:OTHER
// d,x
