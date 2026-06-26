package interptest

import "testing"

// expvar: mirrored on wasm, bridged on native. expvar.Var is an interface whose
// String() is dispatched by expvar's serialization (Map.String, the /debug/vars
// handler); on wasm a custom Var is interpreted, so a native bridge would trap.
// expvar's init also registers an http.HandleFunc, exercising the interpreted
// net/http on wasm. Var names are unique to this test (expvar's registry is
// process-global through the native bridge).
func TestSynthExpvar(t *testing.T) {
	const src = `package main
import ("expvar"; "fmt")
type myVar struct{ n int }
func (m *myVar) String() string { return fmt.Sprintf("%d", m.n*10) }
func main() {
	c := expvar.NewInt("ev_hits")
	c.Add(3)
	c.Add(2)
	fmt.Println("Int:", c.Value(), c.String())
	f := expvar.NewFloat("ev_ratio")
	f.Add(1.5)
	fmt.Println("Float:", f.String())
	fn := expvar.Func(func() any { return 42 })
	fmt.Println("Func:", fn.String())
	m := expvar.NewMap("ev_byPath")
	m.Add("/a", 1)
	m.Add("/a", 4)
	m.Set("custom", &myVar{n: 7})
	fmt.Println("Map:", m.String())
	expvar.Publish("ev_top", &myVar{n: 9})
	var hits, top string
	expvar.Do(func(kv expvar.KeyValue) {
		switch kv.Key {
		case "ev_hits":
			hits = kv.Value.String()
		case "ev_top":
			top = kv.Value.String()
		}
	})
	fmt.Println("Do hits:", hits, "top:", top)
}`
	want := "Int: 5 5\n" +
		"Float: 1.5\n" +
		"Func: 42\n" +
		"Map: {\"/a\": 5, \"custom\": 70}\n" +
		"Do hits: 5 top: 90\n"
	if got := evalOut(t, "expvar.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
