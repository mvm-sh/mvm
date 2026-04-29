// Example: embedding the mvm interpreter in a host Go program.
//
// It shows how to:
//   - construct an interpreter for the Go language spec
//   - expose custom host functions and values to interpreted code
//   - import the bundled stdlib bindings so packages like "fmt" work
//   - evaluate a snippet and read its result back into Go
//
// Run from the repo root:
//
//	go run ./examples/embed
package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all" // registers fmt, strings, ... into stdlib.Values
)

func main() {
	i := interp.NewInterpreter(golang.GoSpec)
	i.SetIO(os.Stdin, os.Stdout, os.Stderr)

	// Bring in the standard library bindings (fmt, strings, strconv, ...).
	i.ImportPackageValues(stdlib.Values)

	// Expose a custom package "host" with two functions and one constant.
	// Interpreted code references them as host.Greet, host.Repeat, host.Answer.
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"host": {
			"Greet":  reflect.ValueOf(greet),
			"Repeat": reflect.ValueOf(strings.Repeat),
			"Answer": reflect.ValueOf(42),
		},
	})

	// AutoImportPackages registers every loaded package under its short name,
	// so the snippet does not need explicit `import` statements (the same
	// shortcut used by `mvm run -e` and the REPL).
	i.AutoImportPackages()

	// Evaluate an expression. Eval returns the value left on top of the VM
	// stack, which for an expression is the expression's value.
	res, err := i.Eval("m:expr", `host.Greet("world") + " " + strings.ToUpper(host.Repeat("ab", 3))`)
	if err != nil {
		fmt.Println("eval error:", err)
		return
	}
	fmt.Println("result:", res.Interface())

	// Eval can be called repeatedly; the interpreter keeps its state across
	// calls, so symbols defined in one Eval are visible to the next.
	if _, err := i.Eval("m:def", `x := host.Answer * 2`); err != nil {
		fmt.Println("def error:", err)
		return
	}
	res, err = i.Eval("m:use", `fmt.Sprintf("x = %d", x)`)
	if err != nil {
		fmt.Println("use error:", err)
		return
	}
	fmt.Println("result:", res.Interface())
}

// greet is a plain Go function exposed to interpreted code.
func greet(name string) string {
	return "hello, " + name + "!"
}
