package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

// Calling a promoted method on a nil pointer receiver derefs nil to reach the
// embedded field. That is a recoverable nil-pointer dereference in Go, not a
// host reflect "Field on zero Value" panic that escapes the interpreter.
// Was github.com/kr/pretty TestGoSyntax ((*VGSWrapper)(nil)).
func TestPromotedMethodNilReceiver(t *testing.T) {
	const wantErr = "runtime error: invalid memory address or nil pointer dereference"
	cases := []struct{ name, src string }{
		// Concrete value embed -> promoted value-receiver method (IfaceCall path).
		{"concrete_embed", `
			type inner struct{ s string }
			func (i inner) Name() string { return "N:" + i.s }
			type outer struct{ inner }
			type namer interface{ Name() string }
			func run() (out string) {
				defer func() {
					if r := recover(); r != nil {
						if e, ok := r.(error); ok { out = e.Error() } else { out = "non-error" }
					}
				}()
				var p *outer = nil
				var n namer = p
				_ = n.Name()
				return "nopanic"
			}
			run()
		`},
		// Embedded interface -> promoted method (EmbedIface path).
		{"iface_embed", `
			type sounder interface{ Sound() string }
			type wrap struct{ sounder }
			func run() (out string) {
				defer func() {
					if r := recover(); r != nil {
						if e, ok := r.(error); ok { out = e.Error() } else { out = "non-error" }
					}
				}()
				var p *wrap = nil
				var s sounder = p
				_ = s.Sound()
				return "nopanic"
			}
			run()
		`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			r, err := intp.Eval(c.name, c.src)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if got := r.String(); got != wantErr {
				t.Errorf("got %q, want %q", got, wantErr)
			}
		})
	}
}
