// Package stubs is the method-signature "shape" catalog (S1..S16) that lets
// interpreted methods satisfy native stdlib interfaces (fmt.Stringer,
// json.Marshaler, sort.Interface, io.Reader, ...) at the reflect boundary.
//
// Each shape has a generated stub pool (pool_s*.go) and a hand-written handler
// dispatcher (registry_s*.go).
// The Attach* wrappers resolve a method's shape to a free stub slot PC, then
// delegate rtype synthesis to runtype.
package stubs

//go:generate go run gen_pools.go
