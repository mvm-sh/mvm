package stdlib

import (
	"sort"
	"strings"
)

// Incompat lists per-package tests that mvm cannot pass for reasons rooted in
// the bridge/interpreter design rather than a fixable bug in mvm's compiler.
// `mvm test` rewrites their entry to a t.Skip(reason) shim so they show as
// SKIP instead of FAIL, keeping the compat-matrix pass ratio honest.
//
// Add an entry only when:
//   - the root cause is an architectural limit (bridge type erasure, reflect
//     adapter frames, native-only protocols) -- not a bug worth chasing, AND
//   - the reason is short enough to land in the SKIP line without noise.
//
// Drop the entry the moment the underlying limitation is fixed.
var Incompat = map[string]map[string]string{
	"crypto": {
		"TestSignMessage": "interpreted type can't satisfy native crypto.Signer via reflect.Call (synth attaches only fixed method shapes)",
	},
	"log/slog": {
		"Example_wrapping":              "runtime.Caller through the reflect.Call adapter masks the user source line (reports .:0)",
		"ExampleSetLogLoggerLevel_slog": "interpreted log and native-bridged slog hold separate default-logger state, so log.Print doesn't route through slog.SetDefault",
		"ExampleSetLogLoggerLevel_log":  "interpreted log and native-bridged slog hold separate default-logger state, so slog.Info/Debug don't interleave with log.Print",
	},
	"io": {
		"TestPipeAllocations": "testing.AllocsPerRun: interpreter call/marshal allocates more than native Pipe()'s 4",
	},
	"reflect": {
		"TestFields": "reflect.StructOf cannot build a struct embedding an unexported-named type (anonymous+PkgPath is rejected); VisibleFields misses its promoted fields",
	},
	"io/fs": {
		"TestReadDirPath":  "reflect.StructOf cannot build struct{FS} (promoted methods of embedded interfaces); mvm types the anon struct at runtime",
		"TestReadFilePath": "reflect.StructOf cannot build struct{FS} (promoted methods of embedded interfaces); mvm types the anon struct at runtime",
	},
	"flag": {
		"TestDefineAfterSet": "runtime.Caller through reflect.Call adapter masks the user call site",
	},
	"sync": {
		"TestIssue76126": "re-execs os.Args[0] with -test.run to observe a child crash; under mvm that is the mvm binary, not a test binary, so the child never panics",
	},
	"os": {
		"TestLargeCopyViaNetwork": "stress test: streams a 10MB random file through a localhost TCP pair; ~15s under the interpreter (no testing.Short path)",
		"TestCopyFileToFile":      "stress test: copies a 1MB random file across a srcStart x dstStart x limit subtest grid; ~17s under the interpreter (no testing.Short path)",
	},

	// testing.AllocsPerRun counts heap allocations of the closure body.
	// Interpreted execution boxes operands and reallocates working storage,
	// so the observed count is always well above the native expectation of
	// 0 or 1. Not a mvm bug -- the test is measuring the host runtime.
	"bytes": {
		"TestNewBufferShallow": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestWriteAppend":      "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestGrow":             "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	},
	"strings": {
		"TestBuilderGrow":            "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0/1",
		"TestBuilderAllocs":          "testing.AllocsPerRun observes mvm interpreter allocations; native expects 1",
		"TestBuilderGrowSizeclasses": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 1",
		"TestIndexRune":              "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestReplace":                "testing.AllocsPerRun observes mvm interpreter allocations; native expects <=1",
	},
	"unicode/utf8": {
		"TestRuneCountNonASCIIAllocation": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	},
	"strconv": {
		"TestAllocationsFromBytes": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	},
	"fmt": {
		"TestCountMallocs": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0-4",
	},
	"testing": {
		"TestAllocsPerRun": "self-test of AllocsPerRun; mvm interpreter allocates more than the native expectation of 1",
	},
	"time": {
		"TestLinkname":       "uses //go:linkname to reach private time funcs; mvm does not parse linkname directives",
		"ExampleDate":        "expects local zone America/Los_Angeles; time's internal ForceUSPacificForTesting init cannot run against the bridge",
		"ExampleTime_Format": "expects local zone America/Los_Angeles; time's internal ForceUSPacificForTesting init cannot run against the bridge",
		"ExampleParse":       "expects local zone America/Los_Angeles; time's internal ForceUSPacificForTesting init cannot run against the bridge",
	},
	"runtime": {
		"TestHeapObjectsCanMove": "uses //go:linkname to reach private runtime.heapObjectsCanMove; mvm does not parse linkname directives",
		"TestPanicNil":           "depends on runtime/metrics + GODEBUG panicnil semantics not modeled by the bridge",
		"TestIssue48807":         "float32(uint64) double-rounds via float64; mvm lacks direct uint64->float32 rounding (Go issue 48807)",
	},
	"runtime/debug": {
		"TestPanicOnFault": "interpreted recover() cannot catch a SetPanicOnFault hardware fault: it surfaces as a raw Go panic from a reflect-driven store, caught by Run's recoverPanic, not routed through the interpreted defer/recover machinery",
		"TestSetGCPercent": "asserts host GC-pacer NextGC thresholds and forced-GC timing; interpreted allocation does not drive the pacer like native code (flaky even natively, SkipFlaky #20076)",
		"TestStack":        "debug.Stack reads the native goroutine stack; an interpreted method runs via reflect.MakeFunc, so frames show reflect/VM internals instead of the runtime/debug_test source the test greps for",
	},

	"github.com/cespare/xxhash/v2": {
		"TestAllocs":       "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0 (escape-analysis assertion)",
		"TestStringAllocs": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0 (escape-analysis assertion)",
	},

	"github.com/google/go-cmp/cmp": {
		"TestDiff/Transformer/CyclicString":  "cmp's recursive-transformer guard fires only after 65536 path steps; interpreted transforms exhaust memory first",
		"TestDiff/Transformer/CyclicComplex": "cmp's recursive-transformer guard fires only after 65536 path steps; interpreted transforms exhaust memory first",
		// Anon struct embedding a method-bearing interface: reflect.StructOf
		// cannot generate promotion wrappers (same limit as io/fs struct{FS}).
		"TestDiff/Comparer/StringerEqual":   "reflect.StructOf does not support methods of embedded interfaces (anon struct{fmt.Stringer} literal)",
		"TestDiff/Comparer/StringerInequal": "reflect.StructOf does not support methods of embedded interfaces (anon struct{fmt.Stringer} literal)",
	},

	"github.com/google/btree": {
		"TestBTreeG":                     "stress test: builds a 10000-key B-tree x10 iterations; minutes under the interpreter (no testing.Short path)",
		"TestBTree":                      "stress test: builds a 10000-key B-tree x10 iterations; minutes under the interpreter (no testing.Short path)",
		"TestCloneConcurrentOperationsG": "stress test: 10000-key concurrent-clone workload; minutes under the interpreter (no testing.Short path)",
		"TestCloneConcurrentOperations":  "stress test: 10000-key concurrent-clone workload; minutes under the interpreter (no testing.Short path)",
	},

	"github.com/yuin/goldmark": {
		"TestDeepNestedLabelPerformance": "wall-clock bound: 50000-deep nested link labels under 5s is a native-speed assertion; ~40s under the interpreter",
	},

	// TODO: revisit; skipped for wall-clock only, not a failure.
	"gonum.org/v1/gonum/mat": {
		"TestDenseMul":           "stress test: testTwoInput type-pair grid of O(n^3) multiplies; element-wise siblings already take ~500s, Mul far longer under the interpreter",
		"TestDenseAdd":           "stress test: testTwoInput type-pair grid; ~580s under the interpreter",
		"TestDenseMulElem":       "too long under the interpreter",
		"TestDenseDivElem":       "too long under the interpreter",
		"TestDenseSub":           "too long under the interpreter",
		"TestHOGSVD":             "too long under the interpreter",
		"TestSolve":              "too long under the interpreter",
		"TestDenseOverlaps":      "too long under the interpreter",
		"TestSymRankK":           "too long under the interpreter",
		"TestTriMul":             "too long under the interpreter",
		"TestSVD":                "too long under the interpreter",
		"TestEqual":              "too long under the interpreter",
		"TestDenseAugment":       "too long under the interpreter",
		"TestDenseStack":         "too long under the interpreter",
		"TestGSVD/{150_200_200}": "too long under the interpreter",
		"TestGSVD/{200_200_150}": "too long under the interpreter",
		"TestGSVD/{150_200_150}": "too long under the interpreter",
		"TestGSVD/{150_150_200}": "too long under the interpreter",
		"TestGSVD/{200_150_150}": "too long under the interpreter",
		"TestGSVD/{150_150_150}": "too long under the interpreter",
		"TestCholeskySymRankOne": "too long under the interpreter",
	},

	"gonum.org/v1/plot": {
		"TestDrawGlyphBoxes": "FMA fusion: the interpreter doesn't fuse a*b+c (native arm64 does), so the text rasterizer's antialiasing differs by <=5/255 at 465 title-edge pixels; mvm's render is byte-identical to the amd64 (non-FMA) golden, which itself differs from glyphbox_arm64 by exactly those pixels",
	},

	"gonum.org/v1/gonum/stat/distuv": {
		"TestGumbelRight":         "too long under the interpreter",
		"TestGamma":               "too long under the interpreter",
		"TestInverseGamma":        "too long under the interpreter",
		"TestF":                   "too long under the interpreter",
		"TestLaplace":             "too long under the interpreter",
		"TestChiSquared":          "too long under the interpreter",
		"TestLognormal":           "too long under the interpreter",
		"TestUniform":             "too long under the interpreter",
		"TestTriangle":            "too long under the interpreter",
		"TestPoisson":             "too long under the interpreter",
		"TestBernoulli":           "too long under the interpreter",
		"TestChi":                 "too long under the interpreter",
		"TestNormal":              "too long under the interpreter",
		"TestExponential":         "too long under the interpreter",
		"TestStudentsT":           "too long under the interpreter",
		"TestBetaRand":            "too long under the interpreter",
		"TestBinomial":            "too long under the interpreter",
		"TestPareto":              "too long under the interpreter",
		"TestWeibull":             "too long under the interpreter",
		"TestAlphaStableGaussian": "too long under the interpreter",
		"TestCategoricalRand":     "too long under the interpreter",
		"TestAlphaStability":      "too long under the interpreter",
		"TestNoncentralTRand":     "too long under the interpreter",
		"TestNoncentralTRand/0":   "too long under the interpreter",
		"TestNoncentralTRand/1":   "too long under the interpreter",
	},

	"gonum.org/v1/gonum/dsp/fourier": {
		"TestCmplxFFT":       "too long under the interpreter",
		"TestCoefficients":   "too long under the interpreter",
		"TestSequence":       "too long under the interpreter",
		"TestQuarterWaveFFT": "too long under the interpreter",
		"TestFFT":            "too long under the interpreter",
		"TestDCT":            "too long under the interpreter",
		"TestDST":            "too long under the interpreter",
	},

	"gonum.org/v1/gonum/graph/path": {
		"TestAStar":                             "too long under the interpreter",
		"TestShortestAlts/AllTo_200×16|0.1":     "too long under the interpreter",
		"TestAllShortest/AllBetween_200×16|0.1": "too long under the interpreter",
	},

	"gonum.org/v1/gonum/lapack/gonum": {
		"TestDormhr":  "too long under the interpreter",
		"TestDgetrf":  "too long under the interpreter",
		"TestDgeqrf":  "too long under the interpreter",
		"TestDbdsqr":  "too long under the interpreter",
		"TestDsyev":   "too long under the interpreter",
		"TestDhseqr":  "too long under the interpreter",
		"TestDgebak":  "too long under the interpreter",
		"TestDgetrs":  "too long under the interpreter",
		"TestDgebal":  "too long under the interpreter",
		"TestDlaqr5":  "too long under the interpreter",
		"TestDsytrd":  "too long under the interpreter",
		"TestDlahqr":  "too long under the interpreter",
		"TestDpotrf":  "too long under the interpreter",
		"TestDgesvd":  "too long under the interpreter",
		"TestDgerqf":  "too long under the interpreter",
		"TestDpbtrs":  "too long under the interpreter",
		"TestDtrexc":  "too long under the interpreter",
		"TestDormbr":  "too long under the interpreter",
		"TestDpbtrf":  "too long under the interpreter",
		"TestDlaexc":  "too long under the interpreter",
		"TestDlahr2":  "too long under the interpreter",
		"TestDgehd2":  "too long under the interpreter",
		"TestDgelqf":  "too long under the interpreter",
		"TestDormlq":  "too long under the interpreter",
		"TestDormqr":  "too long under the interpreter",
		"TestDgels":   "too long under the interpreter",
		"TestDorgtr":  "too long under the interpreter",
		"TestDorglq":  "too long under the interpreter",
		"TestDorgqr":  "too long under the interpreter",
		"TestDorghr":  "too long under the interpreter",
		"TestDgeev":   "too long under the interpreter",
		"TestDgehrd":  "too long under the interpreter",
		"TestDgebrd":  "too long under the interpreter",
		"TTestDorgql": "too long under the interpreter",
		"TestDgetri":  "too long under the interpreter",
		"TestDgeqp3":  "too long under the interpreter",
		"TestDpotri":  "too long under the interpreter",
		"TestDtbtrs":  "too long under the interpreter",
		"TestDpstrf":  "too long under the interpreter",
		"TestDorgql":  "too long under the interpreter",
		"TestDlaqr23": "too long under the interpreter",
		"TestDlaqr04": "too long under the interpreter",
	},

	"gonum.org/v1/gonum/mathext": {
		"TestIncBeta": "too long under the interpreter",
	},

	"gonum.org/v1/gonum/spatial/kdtree": {
		"TestNearestRandom": "too long under the interpreter",
	},

	"gonum.org/v1/gonum/optimize": {
		"TestGradientDescentBacktrackingWithMinimumStepSize": "too long under the interpreter",
		"TestGradientDescentBacktracking":                    "too long under the interpreter",
		"TestGradientDescentBisection":                       "too long under the interpreter",
		"TestGradientDescent":                                "too long under the interpreter",
		"TestCmaEsChol":                                      "too long under the interpreter",
	},

	"github.com/oklog/ulid/v2": {
		"TestLexicographicalOrder": "stress test: quick.Check MaxCount 1e6 (~286s); hardcoded count ignores -short/-quickchecks",
		"TestCompare":              "stress test: quick.CheckEqual MaxCount 1e5 (~42s); hardcoded count ignores -short/-quickchecks",
		"TestRoundTrips":           "stress test: quick.Check MaxCount 1e5 (~25s); hardcoded count ignores -short/-quickchecks",
		"TestEncoding":             "stress test: quick.Check MaxCount 1e5 (~23s); hardcoded count ignores -short/-quickchecks",
	},

	"github.com/stretchr/testify/assert": {
		"TestDirExists":   "upstream-red: asserts ../_codegen exists, but the dir is omitted from the published module (go test fails natively too)",
		"TestNoDirExists": "upstream-red: asserts ../_codegen exists, but the dir is omitted from the published module (go test fails natively too)",
	},

	"github.com/mitchellh/mapstructure": {
		"TestDecode_NilInterfaceHook":    "bridge type erasure: interface-typed struct fields materialize as interface{}, so the hook's to.String() == \"io.Writer\" never matches",
		"TestDecode_DecodeHookInterface": "bridge type erasure: interface-typed struct fields materialize as interface{}, so the hook's to == reflect.TypeOf((*Interface)(nil)).Elem() never matches",
	},

	"github.com/sirupsen/logrus": {
		"TestNestedLoggingReportsCorrectCaller": "asserts caller frame.File == cwd-relative on-disk path; virtualized runtime.Callers reports the modfs source path (func and line do match)",
		"TestCallerReportingOverhead":           "wall-clock bound: 5000 log calls under 1s is a native-speed assertion; interpreted execution exceeds it",
	},

	"github.com/shopspring/decimal": {
		"TestDecimal_QuoRem2":   "stress test: ~1e6 combinatorial QuoRem cases (createDivTestCases); ~30s under the interpreter (no testing.Short path)",
		"TestDecimal_DivRound2": "stress test: ~1e6 combinatorial DivRound cases (createDivTestCases); ~44s under the interpreter (no testing.Short path)",
	},

	"github.com/tidwall/gjson": {
		"TestRandomMany":         "stress test: 5e4 iterations of GetManyBytes over random bytes; ~57s under the interpreter (no testing.Short path)",
		"TestRandomData":         "stress test: 2e6 iterations of GetBytes+Parse over random bytes; ~34s under the interpreter (no testing.Short path)",
		"TestRandomValidStrings": "stress test: 1e5 random-string Get round-trips; ~14s under the interpreter (no testing.Short path)",
		"TestValidRandom":        "stress test: wall-clock bound, validpayload over random bytes for two fixed 3s loops; ~6s under the interpreter",
	},

	"github.com/tidwall/sjson": {
		"TestRandomData": "stress test: 2e6 iterations of SetRaw over random bytes; ~52s under the interpreter (no testing.Short path)",
	},

	"golang.org/x/text/cases": {
		"TestAlloc":        "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestFold":         "testtext.AllocsPerRun assertion inline with correctness checks (which pass); interpreter allocates, native expects 0",
		"TestCaseMappings": "testtext.AllocsPerRun assertion inline with correctness checks (which pass); interpreter allocates, native expects 0",
	},

	"golang.org/x/text/language": {
		"TestBestMatchAlloc": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	},

	"golang.org/x/text/runes": {
		"TestMapAlloc":              "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestRemoveAlloc":           "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestReplaceIllFormedAlloc": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	},

	"golang.org/x/text/unicode/norm": {
		"TestWriter": "stress test: streams the static normTests corpus through Form.Writer across all 16 bufSizes x4 forms; ~150s under the interpreter and ignores testing.Short (the same corpus runs via TestAppend/TestString)",
		"TestReader": "stress test: streams the static normTests corpus through Form.Reader across all 16 bufSizes x4 forms; ~14s under the interpreter and ignores testing.Short (the same corpus runs via TestAppend/TestString)",
	},

	"github.com/golang/protobuf/proto": {
		"TestBufferMarshalAllocs": "testing.AllocsPerRun: interpreter allocations differ between the preallocated-buffer and append paths; native expects identical amortized counts",
	},

	"google.golang.org/protobuf/proto": {
		"TestMarshalAppendAllocations": "testing.AllocsPerRun: interpreter allocations differ between the preallocated-buffer and append paths; native expects identical amortized counts",
		"TestHasExtensionNoAlloc":      "testing.AllocsPerRun: HasExtension allocates under the interpreter; native expects 0",
	},

	"google.golang.org/protobuf/encoding/prototext": {
		"TestMarshalAppendAllocations": "testing.AllocsPerRun: interpreter allocations differ between the preallocated-buffer and append paths; native expects identical amortized counts",
	},

	"google.golang.org/protobuf/encoding/protojson": {
		"TestMarshalAppendAllocations": "testing.AllocsPerRun: interpreter allocations differ between the preallocated-buffer and append paths; native expects identical amortized counts",
	},

	"google.golang.org/grpc/internal/leakcheck": {
		"TestCheckRegisterIgnore": "RegisterIgnoreGoroutine ignores a goroutine by its function name in runtime.Stack(all); an interpreted `go f()` runs on a VM goroutine whose native stack shows vm/reflect frames, not f's name, so the ignore can't match and the ignored leak is counted",
	},
}

// GenericOnly lists stdlib packages with an all-generic API: no reflect bridge
// (cmd/extract emits an empty stub) and no interpreted mirror, so mvm cannot
// load them. Keep in sync with the stub note in gen.go.
var GenericOnly = map[string]bool{
	"crypto/hkdf":   true,
	"crypto/pbkdf2": true,
	"unique":        true,
	"weak":          true,
}

// IsGenericOnly reports whether pkgPath is a generic-only stub package.
func IsGenericOnly(pkgPath string) bool { return GenericOnly[pkgPath] }

// Untestable lists packages whose whole test suite has no viable run under the
// interpreter, so `mvm test` skips them wholesale (exit 0, gray in the matrix).
// Coarser than Incompat, which skips individual tests.
var Untestable = map[string]string{
	"runtime": "native-only: most external tests reference export_test.go symbols absent on the bridge, re-exec subprocesses, or use //go:linkname; the suite cannot complete under the interpreter",
}

// UntestableReason returns the wholesale-skip reason for pkgPath, or "" when
// its tests should run normally.
func UntestableReason(pkgPath string) string { return Untestable[pkgPath] }

// SkipReason returns the recorded reason for skipping testName when running
// `mvm test pkgPath`, or "" if the test should run normally. Subtest-path
// entries (names containing '/') are handled separately by SubtestSkips, so a
// top-level testName never matches one here.
func SkipReason(pkgPath, testName string) string {
	if m, ok := Incompat[pkgPath]; ok {
		return m[testName]
	}
	return ""
}

// SubtestSkip pairs a subtest path with its skip reason.
type SubtestSkip struct{ Name, Reason string }

// SubtestSkips returns pkgPath's Incompat entries whose name is a subtest path
// (contains '/', e.g. "TestX/Sub#03"), sorted by name. The driver skips these via
// testing's -skip so sibling subtests still run.
func SubtestSkips(pkgPath string) []SubtestSkip {
	var out []SubtestSkip
	for name, reason := range Incompat[pkgPath] {
		if strings.Contains(name, "/") {
			out = append(out, SubtestSkip{name, reason})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ShortByDefault lists import paths `mvm test` forces -short on (unless the
// caller set it): their stress tests loop 1e5-1e8 times -- minutes under the
// interpreter. Only list pkgs whose tests scale down under -short, not skip.
var ShortByDefault = map[string]bool{
	"sync/atomic":   true,
	"crypto/subtle": true,
	// ~55s otherwise (a stress loop honoring testing.Short); -short keeps 126
	// tests and runs sub-second, so the compat run no longer flakes to timeout.
	"github.com/gofrs/uuid": true,
	// Inline/block tests re-run Run() on every substring of every input
	// (O(n^2) conversions per case); minutes and multi-GB under the
	// interpreter, but the loop honors testing.Short.
	// -short keeps all 65 tests and runs in seconds at ~35MB.
	"github.com/russross/blackfriday/v2": true,
}

// ForceShort reports whether pkgPath's tests should default to -short.
func ForceShort(pkgPath string) bool { return ShortByDefault[pkgPath] }
