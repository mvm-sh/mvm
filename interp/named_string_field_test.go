package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Setting an untyped string const into a named-string field of a native struct
// (reflect.StructField.Tag is reflect.StructTag) used to panic: reflect.Set
// rejects string as not assignable to reflect.StructTag. setFuncField now
// converts when the value differs from the field type only by name.
func TestNamedStringFieldSet(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "Height", Type: reflect.TypeOf(float64(0)), Tag: ` + "`json:\"height\"`" + `},
	})
	fmt.Println(typ.Field(0).Tag.Get("json"))
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("named_string_field.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "height\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
