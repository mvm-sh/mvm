package interptest

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// evalOut evaluates src and returns its stdout, failing on any eval error.
// Used by tests that assert on printed output rather than the result value.
func evalOut(t *testing.T, name, src string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval(name, src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String()
}
