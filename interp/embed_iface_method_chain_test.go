package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

// A method promoted through an embedded interface returns a named interface;
// chaining a call on that result must compile.
func TestEmbedIfaceMethodChain(t *testing.T) {
	src := `
		type Oneof interface{ Name() string }
		type Field interface{ ContainingOneof() Oneof }
		type Fields interface { Len() int; Get(i int) Field }
		type Message interface{ Fields() Fields }
		type ranger struct{ Message }
		func (m ranger) firstOneof() string {
			fds := m.Fields()
			for i := 0; i < fds.Len(); i++ {
				fd := fds.Get(i)
				if o := fd.ContainingOneof(); o != nil {
					return o.Name()
				}
			}
			return ""
		}
		1 + 1
	`
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	if _, err := intp.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
}
