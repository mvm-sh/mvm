package interptest

import "testing"

// go/types is mirrored on wasm (was dropped via the "go" WasmDrop prefix),
// bridged on native. It dot-imports internal/types/errors (also newly mirrored);
// the dot-import of an interpreted package's symbols into an imported package
// exposed a symGet bug (foreignBareAlias rejected the dot-imported name). This
// type-checks a small program: type inference, method sets, signatures, and
// error reporting via the error-code path.
func TestSynthGoTypes(t *testing.T) {
	const src = `package main
import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)
func main() {
	const code = ` + "`" + `package p
func Add(a, b int) int { return a + b }
var X = Add(1, 2)
var Y = "hello"
type T struct{ N int }
func (t T) Get() int { return t.N }
` + "`" + `
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", code, 0)
	if err != nil { fmt.Println("parse:", err); return }
	conf := types.Config{}
	info := &types.Info{Defs: make(map[*ast.Ident]types.Object)}
	pkg, err := conf.Check("p", fset, []*ast.File{f}, info)
	if err != nil { fmt.Println("check:", err); return }
	fmt.Println("X", pkg.Scope().Lookup("X").Type())
	fmt.Println("Y", pkg.Scope().Lookup("Y").Type())
	tt := pkg.Scope().Lookup("T").Type()
	fmt.Println("T methods", types.NewMethodSet(types.NewPointer(tt)).Len())
	fmt.Println("Add", pkg.Scope().Lookup("Add").Type())

	const bad = "package q\nvar Z int = \"s\"\n"
	bf, _ := parser.ParseFile(fset, "q.go", bad, 0)
	var msg string
	c2 := types.Config{Error: func(e error) { if msg == "" { msg = e.Error() } }}
	_, _ = c2.Check("q", fset, []*ast.File{bf}, nil)
	fmt.Println("err", msg != "")
}`
	want := "X int\n" +
		"Y string\n" +
		"T methods 1\n" +
		"Add func(a int, b int) int\n" +
		"err true\n"
	if got := evalOut(t, "gotypes.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
