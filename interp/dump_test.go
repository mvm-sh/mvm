package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
)

func TestDump(t *testing.T) {
	initProgram := "var a int = 2+1; a"
	intp := interp.NewInterpreter(golang.GoSpec)
	r, e := intp.Eval("m:test", initProgram)
	t.Log(r, e)
	if e != nil {
		t.Fatal(e)
	}

	r, e = intp.Eval("m:test", "a = 100")
	t.Log(r, e)
	if e != nil {
		t.Fatal(e)
	}

	d := intp.Dump()
	t.Log(d)

	intp = interp.NewInterpreter(golang.GoSpec)
	r, e = intp.Eval("m:test", initProgram)
	t.Log(r, e)
	if e != nil {
		t.Fatal(e)
	}

	e = intp.ApplyDump(d)
	if e != nil {
		t.Fatal(e)
	}

	r, e = intp.Eval("m:test", "a = a + 1;a")
	t.Log(r, e)
	if e != nil {
		t.Fatal(e)
	}

	if r.Interface() != int(101) {
		t.Fatalf("unexpected result: %v", r)
	}
}
