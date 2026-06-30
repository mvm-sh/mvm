package interptest

import "testing"

// Three bugs from `mvm test context` interpreted; see parentCancelCtx, valueCtx.Value.

func TestChanDirectionEqual(t *testing.T) {
	src := `package main
import "fmt"
func main() {
	ch := make(chan struct{})
	var r <-chan struct{} = ch
	var s chan<- struct{} = ch
	fmt.Println(ch == r, ch == s, r == ch)
}`
	if got, want := evalOut(t, "chandir.go", src), "true true true\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Mirrors timerCtx: embedded cancelCtx.Value returns &t.cancelCtx, a read-only
// ref, dispatched through an interface and type-asserted.
func TestPromotedUnexportedEmbedAssert(t *testing.T) {
	src := `package main
import "fmt"
type I interface{ Value(k any) any }
var theKey int
type inner struct{ id int }
func (c *inner) Value(k any) any {
	if k == &theKey {
		return c
	}
	return nil
}
type outer struct {
	inner
	extra int
}
func main() {
	var i I = &outer{inner: inner{id: 7}, extra: 1}
	p, ok := i.Value(&theKey).(*inner)
	fmt.Println(ok, p.id)
}`
	if got, want := evalOut(t, "promoembed.go", src), "true 7\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// key1(1) vs key2(1); the struct-field read exercises the native-interface path,
// as valueCtx stores its key in an `any` field.
func TestInterfaceDynamicTypeEqual(t *testing.T) {
	src := `package main
import "fmt"
type key1 int
type key2 int
var k1 = key1(1)
var k2 = key2(1)
type vc struct{ key, val any }
func (c *vc) Value(k any) any {
	if k == c.key {
		return c.val
	}
	return nil
}
func main() {
	var a any = k1
	var b any = k2
	fmt.Println(a == b)
	c := &vc{key: k1, val: "v"}
	fmt.Println(c.Value(k1), c.Value(k2))
}`
	if got, want := evalOut(t, "ifaceeq.go", src), "false\nv <nil>\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
