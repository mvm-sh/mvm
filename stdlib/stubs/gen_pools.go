//go:build ignore

// gen_pools.go regenerates pool_s*.go from the shape catalog below.
// Run via `go run gen_pools.go` from this directory, or `make generate`.
// One stub per slot per shape: each baked-in N produces stubSX_N whose body
// is `return dispatchSX(N, recv...)`.
// All shape signatures take a receiver (unsafe.Pointer) as the first param;
// extra params and result types come from the per-shape spec below.
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"log"
	"os"
)

// poolSize is the default per-shape slot count. Slots are consumed
// monotonically (one per synthesized method, never reclaimed), so a shape's
// size must exceed the most distinct attaches a single process performs.
// 256 covers every shape except S1 (Stringer/Error), whose cumulative attaches
// in the interp test suite exceed it; S1 carries a larger Size below.
const poolSize = 256

type shape struct {
	ID      string   // "S1", "S2", ...
	Params  string   // typed params after the receiver, leading ", " or empty
	ArgList string   // arg names passed to dispatchSX after slot+recv (leading ", " or empty)
	Results string   // result list as written after params: "string", "([]byte, error)"
	Imports []string // extra imports beyond "unsafe" (e.g. "fmt" for fmt.State params)
	Size    int      // pool slots; 0 means the default poolSize
}

func (s shape) size() int {
	if s.Size != 0 {
		return s.Size
	}
	return poolSize
}

var shapes = []shape{
	// S1 (Stringer/Error) is the only shape whose cumulative attaches overflow
	// 256 in the test suite (~271); size generously to absorb suite growth.
	{ID: "S1", Results: "string", Size: 2048},
	{ID: "S2", Results: "([]byte, error)"},
	{ID: "S3", Params: ", data []byte", ArgList: ", data", Results: "error"},
	{ID: "S4", Params: ", target error", ArgList: ", target", Results: "bool"},
	{ID: "S5", Params: ", target any", ArgList: ", target", Results: "bool"},
	{ID: "S6", Results: "error"},
	{ID: "S7", Results: "[]error"},
	{ID: "S8", Results: "int"},                                                 // sort.Interface.Len
	{ID: "S9", Params: ", i, j int", ArgList: ", i, j", Results: "bool"},       // sort.Interface.Less
	{ID: "S10", Params: ", i, j int", ArgList: ", i, j"},                       // sort.Interface.Swap (void)
	{ID: "S11", Params: ", x any", ArgList: ", x"},                             // heap.Interface.Push (void)
	{ID: "S12", Results: "any"},                                                // heap.Interface.Pop
	{ID: "S13", Params: ", p []byte", ArgList: ", p", Results: "(int, error)"}, // io.Reader/Writer
	// fmt.Formatter.Format (void); fmt.State is a non-empty interface, so the
	// stub must carry its exact type for the call ABI.
	{ID: "S14", Params: ", st fmt.State, verb rune", ArgList: ", st, verb", Imports: []string{"fmt"}},
	// xml.Marshaler.MarshalXML / xml.Unmarshaler.UnmarshalXML.
	{ID: "S15", Params: ", e *xml.Encoder, start xml.StartElement", ArgList: ", e, start", Results: "error", Imports: []string{"encoding/xml"}},
	{ID: "S16", Params: ", d *xml.Decoder, start xml.StartElement", ArgList: ", d, start", Results: "error", Imports: []string{"encoding/xml"}},
	{ID: "S17", Results: "(int, bool)"},                             // fmt.State.Width / fmt.State.Precision
	{ID: "S18", Params: ", c int", ArgList: ", c", Results: "bool"}, // fmt.State.Flag
	// fmt.Scanner.Scan; fmt.ScanState is a non-empty interface, so the stub
	// must carry its exact type for the call ABI.
	{ID: "S19", Params: ", st fmt.ScanState, verb rune", ArgList: ", st, verb", Results: "error", Imports: []string{"fmt"}},
	{ID: "S20", Params: ", value string", ArgList: ", value", Results: "error"}, // flag.Value.Set
	{ID: "S21", Results: "bool"}, // flag.boolFlag.IsBoolFlag
	// io/fs cluster.
	{ID: "S22", Results: "int64"},                                                                                          // fs.FileInfo.Size
	{ID: "S23", Results: "fs.FileMode", Imports: []string{"io/fs"}},                                                        // fs.FileInfo.Mode, fs.DirEntry.Type
	{ID: "S24", Results: "time.Time", Imports: []string{"time"}},                                                           // fs.FileInfo.ModTime
	{ID: "S25", Results: "(fs.FileInfo, error)", Imports: []string{"io/fs"}},                                               // fs.DirEntry.Info, fs.File.Stat
	{ID: "S26", Params: ", name string", ArgList: ", name", Results: "(fs.File, error)", Imports: []string{"io/fs"}},       // fs.FS.Open
	{ID: "S27", Params: ", name string", ArgList: ", name", Results: "(fs.FileInfo, error)", Imports: []string{"io/fs"}},   // fs.StatFS.Stat
	{ID: "S28", Params: ", dir string", ArgList: ", dir", Results: "(fs.FS, error)", Imports: []string{"io/fs"}},           // fs.SubFS.Sub
	{ID: "S29", Params: ", pattern string", ArgList: ", pattern", Results: "([]string, error)"},                            // fs.GlobFS.Glob
	{ID: "S30", Params: ", name string", ArgList: ", name", Results: "([]fs.DirEntry, error)", Imports: []string{"io/fs"}}, // fs.ReadDirFS.ReadDir
	{ID: "S31", Params: ", name string", ArgList: ", name", Results: "([]byte, error)"},                                    // fs.ReadFileFS.ReadFile
}

func main() {
	for _, s := range shapes {
		emit(s)
	}
}

func emit(s shape) {
	sz := s.size()
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by gen_pools.go; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Shape %s pool: %d slots.\n\n", s.ID, sz)
	fmt.Fprintf(&b, "package stubs\n\n")
	fmt.Fprintf(&b, "import (\n\t\"unsafe\"\n")
	for _, imp := range s.Imports {
		fmt.Fprintf(&b, "\t%q\n", imp)
	}
	fmt.Fprintf(&b, "\n\t\"github.com/mvm-sh/mvm/runtype\"\n")
	fmt.Fprintf(&b, ")\n\n")
	fmt.Fprintf(&b, "const poolSize%s = %d\n\n", s.ID, sz)
	ret := "return "
	if s.Results == "" {
		ret = "" // void shape: no result to return
	}
	for i := range sz {
		fmt.Fprintf(&b, "//go:noinline\n")
		fmt.Fprintf(&b,
			"func stub%s_%d(recv unsafe.Pointer%s) %s { %sdispatch%s(%d, recv%s) }\n\n",
			s.ID, i, s.Params, s.Results, ret, s.ID, i, s.ArgList)
	}
	fmt.Fprintf(&b, "var stubs%s = [poolSize%s]uintptr{\n", s.ID, s.ID)
	for i := range sz {
		fmt.Fprintf(&b, "\truntype.FuncPC(stub%s_%d),\n", s.ID, i)
	}
	fmt.Fprintf(&b, "}\n")

	formatted, err := format.Source(b.Bytes())
	if err != nil {
		log.Fatalf("format pool_%s.go: %v", lower(s.ID), err)
	}
	name := fmt.Sprintf("pool_%s.go", lower(s.ID))
	if err := os.WriteFile(name, formatted, 0o644); err != nil {
		log.Fatalf("write %s: %v", name, err)
	}
	fmt.Printf("wrote %s\n", name)
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
