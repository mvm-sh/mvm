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
	"strings"
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
}

// wordShapes are the ABI word-class shapes (see word.go): each method param and
// result is classified into pointer words (p) and integer words (f-words omitted
// in stage 1), and one generic dispatcher per word-shape marshals via reflect.
// Params and results are flat class strings over {p,i}; key is "params_results".
// Many distinct Go signatures collapse to one word-shape, so this list -- unlike
// the per-Go-signature shapes above -- does not grow per new signature.
//
//	"_i"       func() <int/uint/bool/named-scalar>   fs.FileInfo.Size/Mode, DirEntry.Type
//	"_pp"      func() <iface>                        general single-interface return
//	"_pppp"    func() (iface, iface)                 fs.FileInfo? no; DirEntry.Info/File.Stat (X, error)
//	"pi_pppp"  func(string) (iface, iface)           fs.FS.Open (File, error)
//	"pi_piipp" func(string) (slice, iface)           fs.ReadDirFS.ReadDir, GlobFS.Glob, ReadFileFS.ReadFile
//	"i_piipp"  func(int) (slice, iface)              fs.ReadDirFile.ReadDir ([]DirEntry, error)
//	"_iip"     func() <word-sized-leaf struct>       fs.FileInfo.ModTime (time.Time = {uint64,int64,*Location})
var wordShapes = []wordShape{
	{Params: "", Results: "i"},
	{Params: "", Results: "pp"},
	{Params: "", Results: "pppp"},
	{Params: "pi", Results: "pppp"},
	{Params: "pi", Results: "piipp"},
	{Params: "i", Results: "piipp"},
	{Params: "", Results: "iip"},
}

// wordShape is one ABI word-class shape. Params and Results are flat class
// strings over {p,i} (p = pointer word, i = integer word), in signature order.
type wordShape struct {
	Params  string
	Results string
	Size    int
}

func (w wordShape) size() int {
	if w.Size != 0 {
		return w.Size
	}
	return poolSize
}

// key is the runtime lookup key the vm computes independently (params_results).
func (w wordShape) key() string { return w.Params + "_" + w.Results }

// ident is the Go-identifier base for this shape's generated symbols.
func (w wordShape) ident() string { return "W" + w.Params + "_" + w.Results }

func main() {
	for _, s := range shapes {
		emit(s)
	}
	for _, w := range wordShapes {
		emitWord(w)
	}
}

// wordParamParts builds the typed param list, the dispatch arg list, the scatter
// statements (each param word into pw[]/sw[]), and the pointer/integer counts.
func wordParamParts(classes string) (decl, args, scatter string, npw, nsw int) {
	for k, c := range classes {
		name := fmt.Sprintf("w%d", k)
		if c == 'p' {
			decl += fmt.Sprintf(", %s unsafe.Pointer", name)
			scatter += fmt.Sprintf("\tpw[%d] = %s\n", npw, name)
			npw++
		} else {
			decl += fmt.Sprintf(", %s uint64", name)
			scatter += fmt.Sprintf("\tsw[%d] = %s\n", nsw, name)
			nsw++
		}
		args += ", " + name
	}
	return decl, args, scatter, npw, nsw
}

// wordResultParts builds the result type list (no names) and the gather return
// statement reading from rpw[]/rsw[], plus the pointer/integer counts.
func wordResultParts(classes string) (decl, gather string, nrpw, nrsw int) {
	if classes == "" {
		return "", "", 0, 0
	}
	var types, vals []string
	for _, c := range classes {
		if c == 'p' {
			types = append(types, "unsafe.Pointer")
			vals = append(vals, fmt.Sprintf("rpw[%d]", nrpw))
			nrpw++
		} else {
			types = append(types, "uint64")
			vals = append(vals, fmt.Sprintf("rsw[%d]", nrsw))
			nrsw++
		}
	}
	return "(" + strings.Join(types, ", ") + ")", "return " + strings.Join(vals, ", "), nrpw, nrsw
}

func emitWord(w wordShape) {
	sz := w.size()
	id := w.ident()
	pDecl, pArgs, scatter, npw, nsw := wordParamParts(w.Params)
	rDecl, gather, nrpw, nrsw := wordResultParts(w.Results)
	ret := ""
	if rDecl != "" {
		ret = "return "
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by gen_pools.go; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// Word-shape %s pool: %d slots.\n\n", w.key(), sz)
	fmt.Fprintf(&b, "package stubs\n\n")
	fmt.Fprintf(&b, "import (\n\t\"sync/atomic\"\n\t\"unsafe\"\n\n\t\"github.com/mvm-sh/mvm/runtype\"\n)\n\n")
	fmt.Fprintf(&b, "const poolSize%s = %d\n\n", id, sz)
	fmt.Fprintf(&b, "var (\n\tslotPool%s [poolSize%s]CoreFunc\n\tnextSlot%s atomic.Uint32\n)\n\n", id, id, id)

	for i := range sz {
		fmt.Fprintf(&b, "//go:noinline\n")
		fmt.Fprintf(&b, "func stub%s_%d(recv unsafe.Pointer%s) %s { %sdispatch%s(%d, recv%s) }\n\n",
			id, i, pDecl, rDecl, ret, id, i, pArgs)
	}

	fmt.Fprintf(&b, "var stubs%s = [poolSize%s]uintptr{\n", id, id)
	for i := range sz {
		fmt.Fprintf(&b, "\truntype.FuncPC(stub%s_%d),\n", id, i)
	}
	fmt.Fprintf(&b, "}\n\n")

	// The per-shape dispatcher: scatter the native words into pw/sw, invoke the
	// vm-supplied core, gather the result words back out.
	fmt.Fprintf(&b, "func dispatch%s(slot uint32, recv unsafe.Pointer%s) %s {\n", id, pDecl, rDecl)
	fmt.Fprintf(&b, "\tvar pw [%d]unsafe.Pointer\n\tvar sw [%d]uint64\n", npw, nsw)
	b.WriteString(scatter)
	fmt.Fprintf(&b, "\tvar rpw [%d]unsafe.Pointer\n\tvar rsw [%d]uint64\n", nrpw, nrsw)
	fmt.Fprintf(&b, "\tif core := slotPool%s[slot]; core != nil {\n", id)
	fmt.Fprintf(&b, "\t\tcore(recv, pw[:], sw[:], rpw[:], rsw[:])\n\t}\n")
	if gather != "" {
		fmt.Fprintf(&b, "\t%s\n", gather)
	}
	fmt.Fprintf(&b, "}\n\n")

	fmt.Fprintf(&b, "func init() {\n\tregisterWordPool(%q, &wordPool{\n", w.key())
	fmt.Fprintf(&b, "\t\tnext:  &nextSlot%s,\n\t\tcap:   poolSize%s,\n", id, id)
	fmt.Fprintf(&b, "\t\tstubs: stubs%s[:],\n\t\tslots: slotPool%s[:],\n\t\tname:  %q,\n\t})\n}\n", id, id, id)

	formatted, err := format.Source(b.Bytes())
	if err != nil {
		log.Fatalf("format pool_%s.go: %v", lower(id), err)
	}
	name := fmt.Sprintf("pool_%s.go", lower(id))
	if err := os.WriteFile(name, formatted, 0o644); err != nil {
		log.Fatalf("write %s: %v", name, err)
	}
	fmt.Printf("wrote %s\n", name)
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
