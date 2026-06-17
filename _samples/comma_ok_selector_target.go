package main

// A 2-value comma-ok RHS (type assertion, map index, channel receive) assigned to
// a non-ident LHS (selector / map index) routes through parseAssignSingleRHSViaTemps,
// which must still mark the RHS comma-ok or the Define underflows its symbol stack
// ("define stack underflow"), or grabs a stale prior-scope `ok` ("undefined: .../ok").
// This is grpc/grpclog SetLoggerV2: `internal.DepthLoggerV2Impl, _ = l.(...)`.

type I interface{ M() }
type J interface {
	M()
	N()
}
type t struct{}

func (t) M() {}
func (t) N() {}

type other struct{}

func (other) M() {}

type holder struct{ d J }

func set(h *holder, l I) {
	if _, ok := l.(other); ok { // a prior if-init `ok`, out of scope below
		panic("no")
	}
	h.d, _ = l.(J) // selector target, blank second, after the if
}

func main() {
	var h holder
	set(&h, t{})
	println("sel:", h.d != nil)

	m := map[string]J{}
	m["k"], _ = I(t{}).(J)
	println("map:", m["k"] != nil)
}

// Output:
// sel: true
// map: true
