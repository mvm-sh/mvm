package main

// Example output extraction. This is the only place that depends on the host
// go/parser + go/doc toolchain: mvm's own parser is comment-free, so the
// expected output of an Example (its `// Output:` directive) must be recovered
// from the raw test source here, separate from the rest of the test runner.

import (
	"go/doc"
	"go/parser"
	"go/token"
	"strings"

	"github.com/mvm-sh/mvm/interp"
)

// exampleEntry is a runnable Example* function paired with its expected output.
type exampleEntry struct {
	name      string
	output    string
	unordered bool
}

// collectExamples returns the loaded Example* functions that have an output
// directive (`// Output:` / `// Unordered output:`), in source-declaration
// order -- the same subset `go test` runs (output-less examples are
// compile-only).
func collectExamples(i *interp.Interp) []exampleEntry {
	meta := map[string]exampleEntry{}
	fset := token.NewFileSet()
	for _, s := range i.Sources {
		if !strings.HasSuffix(s.Name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, s.Name, s.Content(), parser.ParseComments)
		if err != nil {
			continue
		}
		for _, ex := range doc.Examples(f) {
			if ex.Output == "" && !ex.EmptyOutput {
				continue // output-less example: compile-only, like go test
			}
			name := "Example" + ex.Name
			meta[name] = exampleEntry{name: name, output: ex.Output, unordered: ex.Unordered}
		}
	}
	if len(meta) == 0 {
		return nil
	}
	// Keep only examples that loaded as callable funcs, in source-declaration
	// order. FuncNames("Example") can't be used: it requires an uppercase char
	// after the prefix, which rejects the Example_suffix form -- so filter the
	// full FuncNames("") list through meta instead.
	var out []exampleEntry
	for _, name := range i.FuncNames("") {
		if e, ok := meta[name]; ok {
			out = append(out, e)
		}
	}
	return out
}
