package interp

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Regression for `mvm test golang.org/x/net/html` -> reflect.Set panic
// "value of type html.insertionMode is not assignable to type func()".
//
// insertionMode is `func(*parser) bool` and parser has insertionMode fields plus
// an insertionModeStack ([]insertionMode) field: a cycle parser -> im -> func(*parser)
// -> parser. Materializing the func type first leaves it served as the erased func()
// placeholder while parser's layout (struct field and slice elem) is built, baking
// func() in place of the real signature. The leak was materialization-order
// dependent (map iteration), so a fresh interp per iteration eventually hits the
// bad order; loop to make the regression deterministic.
func TestSelfRefFuncFieldMaterialize(t *testing.T) {
	src := `package main

import "fmt"

type token struct {
	kind int
	data string
}

type insertionMode func(*parser) bool

type imodeStack []insertionMode

type parser struct {
	tok        token
	stack      imodeStack
	im         insertionMode
	originalIM insertionMode
	scripting  bool
}

func (s *imodeStack) push(m insertionMode) { *s = append(*s, m) }
func (s *imodeStack) pop() (m insertionMode) {
	i := len(*s)
	m = (*s)[i-1]
	*s = (*s)[:i-1]
	return m
}
func (s *imodeStack) top() insertionMode {
	if i := len(*s); i > 0 {
		return (*s)[i-1]
	}
	return nil
}

func (p *parser) run() bool { return p.im(p) }

func initialIM(p *parser) bool { return true }
func inBodyIM(p *parser) bool  { return false }

func main() {
	p := &parser{scripting: true, im: initialIM}
	p.originalIM = p.im
	p.stack.push(initialIM)
	p.stack.push(inBodyIM)
	p.im = p.stack.top()
	got := p.run()
	m := p.stack.pop()
	p.im = p.stack.top()
	fmt.Println(got, m(p), p.run(), p.originalIM(p))
}
`
	const want = "false false true true\n"
	for iter := range 40 {
		var stdout, stderr bytes.Buffer
		i := NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.ImportPackageConsts(stdlib.ConstValues)
		i.SetIO(os.Stdin, &stdout, &stderr)
		if _, err := i.Eval("selfref_func.go", src); err != nil {
			t.Fatalf("iter %d: Eval: %v\nstderr: %s", iter, err, stderr.String())
		}
		if got := stdout.String(); !strings.HasSuffix(got, want) {
			t.Fatalf("iter %d: stdout: got %q, want suffix %q (stderr: %s)", iter, got, want, stderr.String())
		}
	}
}
