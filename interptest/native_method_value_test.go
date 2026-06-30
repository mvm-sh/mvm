package interptest

import "testing"

// A method value taken off a native (bridged) receiver and STORED (f := x.M)
// rather than called inline must remain callable. IfaceCall's ADR-023 fast path
// pushes a cachedNativeCall marker; storing it into a func/interface slot boxes
// it in an interface{}, so Call must unwrap that box before matching the marker
// rtype. Regression: gonum graph/path crashed via gen.SmallWorldsBB binding
// rnd := r.Float64 off a math/rand/v2 *Rand.
func TestNativeMethodValueStored(t *testing.T) {
	const src = `package main
import (
	"fmt"
	"math/rand/v2"
	"strings"
)
func main() {
	// pointer-receiver method value off a doubly-wrapped *rand.Rand (the gonum case)
	r := rand.New(rand.New(rand.NewPCG(1, 1)))
	rnd := r.Float64
	fmt.Printf("%.4f\n", rnd())

	// value/pointer method values stored then called
	var b strings.Builder
	w := b.WriteString // variadic-free (string)(int,error)
	w("a")
	w("bc")
	f := b.String
	fmt.Println(f())

	// explicitly typed func slot
	var g func() int = b.Len
	fmt.Println(g())
}`
	want := "0.3403\nabc\n3\n"
	if got := evalOut(t, "nmv.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A native method value passed straight to a native higher-order func.
func TestNativeMethodValueAsNativeArg(t *testing.T) {
	const src = `package main
import (
	"fmt"
	"sync"
	"time"
)
func main() {
	var wg sync.WaitGroup
	wg.Add(1)
	time.AfterFunc(time.Millisecond, wg.Done)
	wg.Wait()
	fmt.Println("done")
}`
	want := "done\n"
	if got := evalOut(t, "nmv_arg.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
