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
	// S1 (Stringer/Error) peaks at ~3144 attaches compiling protobuf/proto's
	// test suite (~4.5x recompilation dup over ~692 distinct types).
	{ID: "S1", Results: "string", Size: 4096},
	{ID: "S2", Results: "([]byte, error)"},
	{ID: "S3", Params: ", data []byte", ArgList: ", data", Results: "error", Size: 512},
	{ID: "S4", Params: ", target error", ArgList: ", target", Results: "bool"},
	{ID: "S5", Params: ", target any", ArgList: ", target", Results: "bool"},
	{ID: "S6", Results: "error"},
	{ID: "S7", Results: "[]error"},
	{ID: "S8", Results: "int", Size: 512},                                      // sort.Interface.Len
	{ID: "S9", Params: ", i, j int", ArgList: ", i, j", Results: "bool"},       // sort.Interface.Less
	{ID: "S10", Params: ", i, j int", ArgList: ", i, j"},                       // sort.Interface.Swap (void)
	{ID: "S11", Params: ", x any", ArgList: ", x", Size: 1024},                 // heap.Interface.Push (void)
	{ID: "S12", Results: "any", Size: 512},                                     // heap.Interface.Pop
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
	// S21 (func() bool) is pervasive in generated protobuf descriptors
	// (IsList/IsMap/...): proto's test suite peaks at ~1123 attaches.
	{ID: "S21", Results: "bool", Size: 2048}, // flag.boolFlag.IsBoolFlag
	// io/fs cluster.
	{ID: "S22", Results: "int64", Size: 512},                                                                               // fs.FileInfo.Size
	{ID: "S23", Results: "fs.FileMode", Imports: []string{"io/fs"}},                                                        // fs.FileInfo.Mode, fs.DirEntry.Type
	{ID: "S24", Results: "time.Time", Imports: []string{"time"}},                                                           // fs.FileInfo.ModTime
	{ID: "S25", Results: "(fs.FileInfo, error)", Imports: []string{"io/fs"}},                                               // fs.DirEntry.Info, fs.File.Stat
	{ID: "S26", Params: ", name string", ArgList: ", name", Results: "(fs.File, error)", Imports: []string{"io/fs"}},       // fs.FS.Open
	{ID: "S27", Params: ", name string", ArgList: ", name", Results: "(fs.FileInfo, error)", Imports: []string{"io/fs"}},   // fs.StatFS.Stat
	{ID: "S28", Params: ", dir string", ArgList: ", dir", Results: "(fs.FS, error)", Imports: []string{"io/fs"}},           // fs.SubFS.Sub
	{ID: "S29", Params: ", pattern string", ArgList: ", pattern", Results: "([]string, error)"},                            // fs.GlobFS.Glob
	{ID: "S30", Params: ", name string", ArgList: ", name", Results: "([]fs.DirEntry, error)", Imports: []string{"io/fs"}}, // fs.ReadDirFS.ReadDir
	{ID: "S31", Params: ", name string", ArgList: ", name", Results: "([]byte, error)"},                                    // fs.ReadFileFS.ReadFile
	// log/slog cluster (slog.Handler).
	{ID: "S32", Params: ", ctx context.Context, level slog.Level", ArgList: ", ctx, level", Results: "bool", Imports: []string{"context", "log/slog"}},     // slog.Handler.Enabled
	{ID: "S33", Params: ", ctx context.Context, record slog.Record", ArgList: ", ctx, record", Results: "error", Imports: []string{"context", "log/slog"}}, // slog.Handler.Handle
	{ID: "S34", Params: ", attrs []slog.Attr", ArgList: ", attrs", Results: "slog.Handler", Imports: []string{"log/slog"}},                                 // slog.Handler.WithAttrs
	{ID: "S35", Params: ", name string", ArgList: ", name", Results: "slog.Handler", Imports: []string{"log/slog"}},                                        // slog.Handler.WithGroup
	{ID: "S36", Results: "slog.Value", Imports: []string{"log/slog"}},                                                                                      // slog.LogValuer.LogValue
	{ID: "S37", Results: "(rune, int, error)"}, // io.RuneReader.ReadRune
	// S38 (niladic markers Reset/ProtoMessage) is pervasive in generated protobuf
	// code: proto's test suite peaks at ~4733 attaches (~3.5x recompilation dup).
	{ID: "S38", Size: 8192},
}

// wordShapes are the ABI word-class shapes: params and results as flat class
// strings over {p,i} (p = pointer word, i = integer word), key "params_results".
// Many Go signatures collapse to one word-shape, so the list does not grow per
// signature; grow it from the MVM_WORDDROPS report. See ADR-022 and
// docs/modules/stubs.md.
var wordShapes = []wordShape{
	// W_pp (niladic 2-pointer-word result) and W_i (niladic int-word result)
	// dominate descriptor-heavy code: protobuf/proto's test suite peaks at ~2571
	// W_pp / ~2143 W_i attaches (~5x recompilation dup). Slots are monotonic and
	// never reclaimed; size for the largest single-process attach count.
	{Params: "", Results: "i", Size: 4096},
	{Params: "", Results: "pp", Size: 4096},
	{Params: "", Results: "pppp"},
	{Params: "pi", Results: "pppp"},
	{Params: "pi", Results: "piipp"},
	{Params: "i", Results: "piipp"},
	{Params: "", Results: "piipp"},
	{Params: "", Results: "pi"},
	{Params: "pii", Results: "ipp"},
	{Params: "", Results: "iip"},
	{Params: "pi", Results: "i"},
	{Params: "p", Results: "i"},
	{Params: "pp", Results: "i"},
	{Params: "i", Results: "i"},
	// http.RoundTripper.RoundTrip: func(*http.Request) (*http.Response, error).
	{Params: "p", Results: "ppp"},
	// http.ResponseWriter.Header + protobuf func() *T getters (niladic ptr, like _pp/_i).
	{Params: "", Results: "p", Size: 4096},
	// http.ResponseWriter.WriteHeader(int) and func(int) setters.
	{Params: "i", Results: "", Size: 1024},
	// net.Conn.SetDeadline/SetReadDeadline/SetWriteDeadline(time.Time) error
	// (time.Time = 3 words iip; error = pp).
	{Params: "iip", Results: "pp"},
	// net/http newClientConner.NewClientConn(net.Conn, func()) (http.RoundTripper, error).
	{Params: "ppp", Results: "pppp"},
	// grpc handler satisfaction (*health.Server -> HealthServer): unary Check/List
	// func(ctx, *Req) (*Resp, error) = ppp_ppp; Watch func(*Req, stream) error = ppp_pp.
	{Params: "ppp", Results: "ppp"},
	{Params: "ppp", Results: "pp"},
	// grpc streaming handlers + metadata/error-returning iface methods:
	// func(stream) error = pp_pp. ~202 attaches in one grpc bidi program; 1024 = headroom.
	{Params: "pp", Results: "pp", Size: 1024},
	// func(*testing.T) value-receiver subtest methods (grpctest.RunSubTests),
	// enumerated by reflect; pervasive across grpc test suites.
	{Params: "p", Results: "", Size: 1024},
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
