package derive

import "reflect"

// ShapeAvailable reports whether a method signature has a synth dispatch stub.
// The vm sets it from stdlib/stubs; default-false keeps derive usable standalone.
var ShapeAvailable = func(_ reflect.Type) bool { return false }

// ActiveRtypeCache returns a pointer to the running Machine's rtype dedup cache.
// The pointer (not the map) lets the reserve gate lazy-create it under derivedMu.
// The vm sets it; default-nil disables cross-Eval sharing.
var ActiveRtypeCache = func() *map[MethodStructKey]*SynthReservation { return nil }
