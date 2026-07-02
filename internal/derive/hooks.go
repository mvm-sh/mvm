package derive

import "reflect"

// ShapeAvailable reports whether a method signature has a synth dispatch stub.
// The vm sets it from stdlib/stubs; default-false keeps derive usable standalone.
var ShapeAvailable = func(_ reflect.Type) bool { return false }

// ActiveRtypeCache returns a pointer to the running Machine's rtype dedup cache.
// The pointer (not the map) lets the reserve gate lazy-create it under derivedMu.
// The vm sets it; default-nil disables cross-Eval sharing.
var ActiveRtypeCache = func() *map[MethodStructKey]*SynthReservation { return nil }

// ShareMethodCarriers extends the struct rtype dedup (ActiveRtypeCache) to
// method-bearing named non-struct types (token.Pos). The vm sets it true on wasm,
// where the cache is global and a synth fill captures no *Machine.
var ShareMethodCarriers = false

// IfaceShapeLog records each erased iface-method signature consulted for
// synth-shape availability; the vm sets it under MVM_IFACESHAPES.
// The logged keys are the word pools interface satisfaction actually needs;
// pools outside this set only serve attach traffic that reflect.Implements
// never observes.
var IfaceShapeLog func(sig reflect.Type)
