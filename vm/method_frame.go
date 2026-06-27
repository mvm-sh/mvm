package vm

// methodFrame fuses an interpreted method dispatch's receiver cell and its
// single-entry heap into one allocation, carried to Call as a pointer instead of
// a reflect-boxed Closure (ADR-023). One alloc per dispatch instead of three.
type methodFrame struct {
	code int
	cell Value
	slot [1]*Value
}

// fusedMethodFrame gates the single-alloc receiver path; the benchmark toggles it.
var fusedMethodFrame = true

// SetFusedMethodFrame toggles the fused method-dispatch receiver frame (ADR-023).
func SetFusedMethodFrame(b bool) { fusedMethodFrame = b }
