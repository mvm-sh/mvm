package vm

import (
	"reflect"
	"testing"
	"time"
)

// Cyclic pointer graphs (e.g. testing.T parent/sub links) must not blow up
// into an exponential re-walk bounded only by maxUnboxDepth.
func TestDeepUnboxIfaceCycle(t *testing.T) {
	type node struct {
		A, B *node
		V    any
	}
	n1, n2 := &node{}, &node{}
	n1.A, n1.B = n2, n2
	n2.A, n2.B = n1, n1

	m := &Machine{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.deepUnboxIface(reflect.ValueOf(n1), 0, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deepUnboxIface did not terminate on a cyclic graph")
	}
}
