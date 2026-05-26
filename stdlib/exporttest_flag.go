package stdlib

import (
	"flag"
	"io"
	"os"
	"reflect"
)

// flag's export_test.go injects ResetForTesting and DefaultUsage into the
// flag package for use by its external _test.go file.
// flag is a native bridge here so those test-only symbols don't exist;
// reproduce them from the public API so flag_test.go can load and run.
func init() {
	TestValues["flag"] = map[string]reflect.Value{
		"DefaultUsage": reflect.ValueOf(flag.Usage),
		"ResetForTesting": reflect.ValueOf(func(usage func()) {
			flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)
			flag.CommandLine.Usage = func() { flag.Usage() }
			flag.Usage = usage
		}),
	}
}
