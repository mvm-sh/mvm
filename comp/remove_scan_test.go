package comp

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/vm"
)

// removeGetGlobal and its siblings retract a load the CURRENT generate() pass emitted.
// The backward scan must stop at genStart so a CallImm lowering in one function cannot nop a live GetGlobal belonging to an already-compiled function that loads the same global slot.
// (The grpc NewFramer `debugWriteLoggerf: log.Printf` corruption: a later `*Server.printf` method call reached back across the decl boundary.)
func TestRemoveGetGlobalRespectsGenStart(t *testing.T) {
	const slot = 5

	// A prior function's live GetGlobal at code[0]; the current decl starts at 1.
	c := &Compiler{}
	c.Code = vm.Code{
		{Op: vm.GetGlobal, A: slot}, // prior function -- must NOT be touched
		{Op: vm.GetGlobal, A: slot}, // current decl's own load
	}
	c.genStart = 1
	if !c.removeGetGlobal(slot) {
		t.Fatal("removeGetGlobal: expected to nop the current decl's load")
	}
	if c.Code[0].Op != vm.GetGlobal {
		t.Errorf("prior function's GetGlobal was nopped (crossed genStart): %v", c.Code[0].Op)
	}
	if c.Code[1].Op != vm.Nop {
		t.Errorf("current decl's GetGlobal not nopped: %v", c.Code[1].Op)
	}

	// No GetGlobal at/after genStart: return false (caller falls back to Call), never reach back into the prior function.
	c2 := &Compiler{}
	c2.Code = vm.Code{
		{Op: vm.GetGlobal, A: slot}, // prior function only
		{Op: vm.Pop, A: 1},
	}
	c2.genStart = 1
	if c2.removeGetGlobal(slot) {
		t.Fatal("removeGetGlobal: must not reach a GetGlobal before genStart")
	}
	if c2.Code[0].Op != vm.GetGlobal {
		t.Errorf("prior function's GetGlobal was nopped (crossed genStart): %v", c2.Code[0].Op)
	}
}

// patchNilFnewLen fills a composite literal's nil slice/map Fnew (B=-1) with its length.
// Bounded at genStart so a literal in one function cannot patch a prior function's still-nil container Fnew of the same slot/type into a non-nil slice (the cross-decl analogue of the removeGetGlobal corruption).
func TestPatchNilFnewLenRespectsGenStart(t *testing.T) {
	const slot = 3

	c := &Compiler{}
	c.Code = vm.Code{
		{Op: vm.Fnew, A: slot, B: -1}, // prior function's nil container -- keep B=-1
		{Op: vm.Fnew, A: slot, B: -1}, // current decl's literal
	}
	c.genStart = 1
	c.patchNilFnewLen(slot, nil, 4)
	if c.Code[0].B != -1 {
		t.Errorf("prior function's nil Fnew was patched (crossed genStart): B=%d", c.Code[0].B)
	}
	if c.Code[1].B != 4 {
		t.Errorf("current decl's Fnew not patched: B=%d", c.Code[1].B)
	}

	// Only a prior nil Fnew: must be left untouched.
	c2 := &Compiler{}
	c2.Code = vm.Code{
		{Op: vm.Fnew, A: slot, B: -1}, // prior function only
		{Op: vm.Pop, A: 1},
	}
	c2.genStart = 1
	c2.patchNilFnewLen(slot, nil, 9)
	if c2.Code[0].B != -1 {
		t.Errorf("prior function's nil Fnew was patched (crossed genStart): B=%d", c2.Code[0].B)
	}
}

// fixPtrFnewE turns an FnewE (new-elem for a nil pointer) back into Fnew, bounded at genStart so it cannot rewrite a prior function's FnewE.
func TestFixPtrFnewERespectsGenStart(t *testing.T) {
	const slot = 7
	ptrType := &vm.Type{Rtype: reflect.PointerTo(reflect.TypeOf(0))}

	c := &Compiler{}
	c.Code = vm.Code{
		{Op: vm.FnewE, A: slot}, // prior function -- must NOT be rewritten
		{Op: vm.FnewE, A: slot}, // current decl
	}
	c.genStart = 1
	c.fixPtrFnewE(ptrType, slot)
	if c.Code[0].Op != vm.FnewE {
		t.Errorf("prior function's FnewE was rewritten (crossed genStart): %v", c.Code[0].Op)
	}
	if c.Code[1].Op != vm.Fnew {
		t.Errorf("current decl's FnewE not rewritten to Fnew: %v", c.Code[1].Op)
	}

	// Only a prior FnewE: must be left untouched (the discriminating case -- without the bound the scan reaches back and rewrites it).
	c2 := &Compiler{}
	c2.Code = vm.Code{
		{Op: vm.FnewE, A: slot}, // prior function only
		{Op: vm.Pop, A: 1},
	}
	c2.genStart = 1
	c2.fixPtrFnewE(ptrType, slot)
	if c2.Code[0].Op != vm.FnewE {
		t.Errorf("prior function's FnewE was rewritten (crossed genStart): %v", c2.Code[0].Op)
	}
}
