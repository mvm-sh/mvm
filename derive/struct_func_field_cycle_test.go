package derive

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/mtype"
)

// Regression for `mvm test gorm.io/gorm`: clause.Clause has a func field of type
// ClauseBuilder = func(Clause, Builder), so the struct reaches itself by value
// through the func signature. The Builder interface param keeps Clause pending
// past the first materialize pass (its sigs are unsynthable), so FinalizeDeferred
// re-entered materialize(Clause) while pending; without a stack-scoped guard the
// field -> funcIO -> struct recursion overflowed the stack. The reservation must
// return its in-flight identity on re-entry instead of recursing.
func TestStructFuncFieldByValueCycleMaterialize(t *testing.T) {
	// Builder interface { Append() Fwd }, Fwd a still-forward struct: Append's sig
	// cannot materialize yet, so Builder has no synth rtype and the func field's IO
	// erases, keeping Clause pending -- exactly the gorm path.
	fwd := mtype.SymStruct(nil, nil, nil)
	fwd.Name = "Fwd"
	fwd.PkgName = "example.com/clausecycle"
	fwd.Placeholder = true
	builder := mtype.SymBasic(reflect.Interface)
	builder.Name = "Builder"
	builder.PkgName = "example.com/clausecycle"
	builder.IfaceMethods = []mtype.IfaceMethod{
		{Name: "Append", ID: -1, Rtype: nil, Sig: mtype.SymFunc(nil, []*mtype.Type{fwd}, false)},
	}

	// Clause struct, built first so the func type can reference it by value.
	clause := mtype.SymStruct(nil, nil, nil)
	clause.Name = "Clause"
	clause.PkgName = "example.com/clausecycle"

	// ClauseBuilder = func(Clause, Builder); the field is a clone with the field
	// name in .Name and Base set, as goparser builds it.
	funcType := mtype.SymFunc([]*mtype.Type{clause, builder}, nil, false)
	field := *funcType
	field.Name = "Build"
	field.Base = funcType
	field.Defined = false

	name := mtype.SymBasic(reflect.String)
	name.Name = "Name"
	clause.Fields = []*mtype.Type{name, &field}

	rt := MaterializeRtype(clause)
	if rt == nil {
		t.Fatal("Clause did not materialize")
	}
	if rt.Kind() != reflect.Struct {
		t.Fatalf("Clause rtype = %v, want struct", rt)
	}
	if !isPending(clause) {
		t.Fatal("Clause should be pending while its func field's iface IO is unsynthable")
	}

	// Builder.Append's sig fills; FinalizeDeferred must re-materialize Clause
	// (cycle through the func field) without overflowing.
	builder.IfaceMethods[0].Rtype = reflect.TypeFor[func() bool]()
	FinalizeDeferred()

	if isPending(clause) {
		t.Fatal("Clause should no longer be pending after the func field synths")
	}
	if _, ok := rt.FieldByName("Build"); !ok {
		t.Fatal("no field Build after finalize")
	}
}
