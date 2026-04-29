package interp_test

import (
	"fmt"
	"reflect"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// ExampleInterp_Eval shows how to embed the interpreter, expose custom Go
// functions to interpreted code via a synthetic package, and read back the
// value of an evaluated expression.
func ExampleInterp_Eval() {
	i := interp.NewInterpreter(golang.GoSpec)

	// Bring in the standard library bindings (fmt, strings, ...).
	i.ImportPackageValues(stdlib.Values)

	// Expose a custom package "host" with a function and a constant.
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"host": {
			"Greet":  reflect.ValueOf(func(s string) string { return "hello, " + s + "!" }),
			"Answer": reflect.ValueOf(42),
		},
	})

	// Register every loaded package under its short name so the snippet does
	// not need explicit import statements.
	i.AutoImportPackages()

	res, err := i.Eval("m:expr", `fmt.Sprintf("%s answer=%d", host.Greet("world"), host.Answer)`)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.Interface())
	// Output:
	// hello, world! answer=42
}
