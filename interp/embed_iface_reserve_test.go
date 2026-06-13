package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

// A struct embedding a method-bearing interface needs a synth reservation so the
// promoted EmbedIface methods can be attached (a multi-field struct gets no
// reflect.StructOf method promotion). The reserve gate skipped interface embeds,
// betting on StructOf promotion; when the struct was materialized before
// propagateEmbeddedMethods filled its method table (here FileImport is reached
// via the FileImports.Get signature during materializeIfaceMethods, ahead of the
// first propagate), it stayed unreserved and attach failed with
// "synth: value-method type ... has no reservation at attach".
// Was google.golang.org/protobuf/reflect/protoreflect (FileImport).
func TestEmbedIfaceReservedBeforePropagate(t *testing.T) {
	src := `
		type isDesc interface{ ProtoType() }
		type baseA interface { Name() string; Path() string }
		type baseB interface { Index() int; FullName() string }
		type FileDescriptor interface {
			baseA
			baseB
			isDesc
		}
		type FileImport struct {
			FileDescriptor
			IsPub  bool
			IsWeak bool
		}
		type FileImports interface {
			Len() int
			Get(i int) FileImport
		}
		var gFI = FileImport{IsWeak: true}
		func get() bool { return gFI.IsWeak }
		get()
	`
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	r, err := intp.Eval("test", src)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got := r.Bool(); got != true {
		t.Errorf("got %v, want true", got)
	}
}
