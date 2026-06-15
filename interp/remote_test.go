package interp

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// remoteModule represents a single module/version served by startFakeProxy.
type remoteModule struct {
	path    string
	version string
	files   map[string]string
}

// startFakeProxy stands up an httptest server speaking enough of the Go
// module proxy protocol to satisfy modfs: @latest -> {Version}, and
// /@v/<ver>.zip -> the module zip. Returns the server URL and a counter
// of handled requests for use in assertions.
func startFakeProxy(t *testing.T, modules ...remoteModule) (string, *int64) {
	t.Helper()
	byPath := map[string]remoteModule{}
	for _, m := range modules {
		byPath[m.path] = m
	}
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		path := strings.TrimPrefix(r.URL.Path, "/")
		switch {
		case strings.HasSuffix(path, "/@latest"):
			modPath := strings.TrimSuffix(path, "/@latest")
			m, ok := byPath[modPath]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": m.version})
		case strings.HasSuffix(path, ".zip"):
			rest := strings.TrimSuffix(path, ".zip")
			i := strings.LastIndex(rest, "/@v/")
			if i < 0 {
				http.NotFound(w, r)
				return
			}
			modPath, ver := rest[:i], rest[i+len("/@v/"):]
			m, ok := byPath[modPath]
			if !ok || m.version != ver {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(buildZip(t, modPath, ver, m.files))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &requests
}

// TestRemoteImport runs interpreted code that imports a package fetched
// dynamically over HTTP via modfs. No filesystem source for the package
// exists; the only path is through the network-backed FS.
func TestRemoteImport(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/foo/bar",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod":         "module example.com/foo/bar\n",
			"greet/greet.go": "package greet\nfunc Hello() string { return \"hello from remote\" }\n",
		},
	})

	src := `package main

import "example.com/foo/bar/greet"

func main() {
	println(greet.Hello())
}
`

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "hello from remote\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteTransitiveImport verifies that a remote module's own imports
// of another remote module also resolve through modfs. Module A imports
// module B; the interpreted main only mentions A.
func TestRemoteTransitiveImport(t *testing.T) {
	url, requests := startFakeProxy(t,
		remoteModule{
			path:    "example.com/lib/a",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/lib/a\n",
				"a.go": `package a

import "example.com/lib/b"

func Greet() string { return "A says: " + b.Name() }
`,
			},
		},
		remoteModule{
			path:    "example.com/lib/b",
			version: "v2.3.4",
			files: map[string]string{
				"go.mod": "module example.com/lib/b\n",
				"b.go":   "package b\nfunc Name() string { return \"B\" }\n",
			},
		},
	)

	src := `package main

import "example.com/lib/a"

func main() {
	println(a.Greet())
}
`

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "A says: B\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}

	// Two modules x (latest + zip) = 4 successful requests, plus probe
	// misses for example.com/lib (shortest-first probing tries the
	// 2-component prefix first). Assert at least 4 to confirm both
	// modules were fetched.
	if got := atomic.LoadInt64(requests); got < 4 {
		t.Errorf("expected >= 4 proxy requests (2 modules x latest+zip), got %d", got)
	}
}

// TestRemoteTypeNameCollision exercises the bare-name type-collision path that
// used to SIGSEGV inside vm.patchRtype: package "outer" declares `type Code
// struct{...}` (pre-registered as a fresh struct placeholder) and imports
// package "inner" which declares `type Code uint16` (whose Rtype is the shared,
// read-only reflect.TypeOf(uint16(0))). Parsing inner overwrites the bare
// "Code" symbol with the static rtype; finalizing outer's struct then memcpy'd
// onto read-only memory. With the fix, outer's struct gets a fresh placeholder.
func TestRemoteTypeNameCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":   "module example.com/x/inner\n",
				"inner.go": "package inner\n\ntype Code uint16\n\nconst English Code = 1\n",
			},
		},
		remoteModule{
			path:    "example.com/x/outer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/outer\n",
				"outer.go": `package outer

import "example.com/x/inner"

type Code struct {
	c inner.Code
}

var Zero Code
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	// Pre-fix this Eval faulted with SIGSEGV inside vm.patchRtype, taking down
	// the test binary; the fix makes it return cleanly.
	if _, err := i.Eval("test", `import "example.com/x/outer"; _ = outer.Zero`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
}

// TestRemoteTypedConstMethod checks that a typed string/basic constant keeps
// its named type's method set across a package boundary (x/text's
// internal/language does `const lang tag.Index = "..."; lang.Index(key)` where
// tag.Index is `type Index string`). mvm previously reported "undefined: Index"
// because symbol.MethodByName had no case for symbol.Const.
func TestRemoteTypedConstMethod(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/idx",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/idx\n",
			"idx.go": `package idx

type Index string

func (s Index) Elem(i int) byte { return s[i] }

const Tab Index = "abc"

func Get() byte { return Tab.Elem(1) }
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", `import "example.com/x/idx"; println(idx.Get())`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "98\n"; got != want { // 'b'
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteMethodReceiverTypeCollision checks that a method body binds its
// receiver to the type it was declared against, even when a sibling import
// later shadows the bare type name. Here "inner" declares `type Tag struct{ N
// int }` with method Get; "shadow" also declares `type Tag struct{ S string }`.
// main imports inner then shadow, so by the time inner.Get's deferred body is
// compiled the bare name "Tag" points at shadow's struct. mvm previously bound
// inner.Get's receiver to that wrong Tag and failed with "undefined: N".
func TestRemoteMethodReceiverTypeCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":   "module example.com/x/inner\n",
				"inner.go": "package inner\n\ntype Tag struct{ N int }\n\nfunc (t Tag) Get() int { return t.N }\n",
			},
		},
		remoteModule{
			path:    "example.com/x/shadow",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":    "module example.com/x/shadow\n",
				"shadow.go": "package shadow\n\ntype Tag struct{ S string }\n",
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	// Importing shadow shadows the bare "Tag"; without the Phase-1 receiver-type
	// cache, inner.Get's body would bind t to shadow.Tag and fail "undefined: N".
	// (Note: `inner.Tag{N: 7}.Get()` would also work here for the receiver-type
	// fix but trips a separate composite-literal/shadowing bug, so use a var.)
	src := `import "example.com/x/inner"; import "example.com/x/shadow"; var _ = shadow.Tag{}; var x inner.Tag; x.N = 7; println(x.Get())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteVarNameCollision: two imported packages each declare a top-level
// `var data` of a different type. Phase 2 compiles each package's var init and
// then a function body that reads its own `data`. Before package-qualified
// resolution of deferred declarations, the bare key "data" in the symbol table
// was shared and its symbol mutated in place by whichever var init compiled
// last, so inner.Sum's body would see shadow's `data` ([]int) and fail
// "undefined: n" on `data[i].n`.
func TestRemoteVarNameCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner\n",
				"inner.go": `package inner

type rec struct{ n int }

var data = [3]rec{{n: 2}, {n: 3}, {n: 5}}

func Sum() int {
	s := 0
	for i := range data {
		s += data[i].n
	}
	return s
}
`,
			},
		},
		remoteModule{
			path:    "example.com/x/shadow",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/shadow\n",
				"shadow.go": `package shadow

var data = []int{1, 2, 3}

func Len() int { return len(data) }
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/inner"; import "example.com/x/shadow"; println(inner.Sum(), shadow.Len())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "10 3\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteTypeNameStructInterfaceCollision: package "inner" declares `type
// Foo struct{ v [8]byte }` and "shadow" declares `type Foo interface{...}`.
// Both keys arrive bare in the symbol table; preRegisterTypes for shadow used
// to call registerInterfacePlaceholder, see inner's already-finalized struct
// symbol, and return it as the "interface placeholder". parseTypeLine then
// adopted shadow's interface Rtype into inner's *vm.Type, flipping the struct
// to an interface. Phase 2 compilation of inner.Make's body (which reads f.v)
// then failed "undefined: v" because the qualified alias
// `example.com/x/inner.Foo` pointed at the flipped type. The fix tightens
// registerInterfacePlaceholder to only reuse a genuine pending interface
// placeholder; otherwise it shadows the bare key with a fresh placeholder.
func TestRemoteTypeNameStructInterfaceCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner\n",
				"inner.go": `package inner

type Foo struct{ v [8]byte }

func Make() Foo {
	var f Foo
	f.v[0] = 'x'
	return f
}

func First(f Foo) byte { return f.v[0] }
`,
			},
		},
		remoteModule{
			path:    "example.com/x/shadow",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/shadow\n",
				"shadow.go": `package shadow

type Foo interface {
	Subtag() string
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	// Importing shadow after inner used to flip inner.Foo from struct to
	// interface; inner.Make's body would then fail "undefined: v".
	src := `import "example.com/x/inner"; import _ "example.com/x/shadow"; println(inner.First(inner.Make()))`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "120\n"; got != want { // 'x'
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteFuncNameCollisionAcrossPkgs: two imported packages each declare a
// top-level `func Make` with different signatures. Before the per-pkg
// registerFunc fix, the bare-key `Make` Symbol from `lo` survived; `mid`'s
// registerFunc short-circuited and never built its own Symbol with the right
// InNames/OutNames. Phase-2 deferred body of mid.Make then read the wrong
// Symbol's params, never registered the named return as a local, and any
// reference to it resolved to whatever bare-key sibling shadowed it (in the
// real golang.org/x/text case: `tag.<field>` resolved to the imported `tag`
// package -- "symbol not found in package <pkg>: <field>"). Also exercises the
// matching Phase-2 fixes: parseFunc looks up funcs via CompilingPkg, and the
// compiler's Label/Goto/fixList paths qualify label keys (Make_end shared the
// bare key across pkgs and resolved Goto Make_end to the wrong pc -- VM looped).
func TestRemoteFuncNameCollisionAcrossPkgs(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/lo",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/lo\n",
				"lo.go":  "package lo\n\nfunc Make(s string) int { _ = s; return 1 }\n",
			},
		},
		remoteModule{
			path:    "example.com/x/mid",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/mid\n",
				"mid.go": `package mid

import "example.com/x/lo"

type Tag struct{ field string }

func Make() (tag Tag) {
	_ = lo.Make("hi")
	tag.field = "ok"
	return tag
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/mid"; t := mid.Make(); println(t.field)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "ok\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteTransitiveImportBareKeyClobber: outer.Foo is a wrapper struct
// referencing inner.Foo (a uint16-underlying type with the same bare name).
// Outer also has a method whose return type list mentions the BARE name `Foo`
// (the outer struct). Pre-fix, importSrc ran preRegisterTypes first (setting
// bare `Foo` to a fresh struct placeholder), then the ParseDecl loop hit
// `import "inner"` and synchronously parsed inner's `type Foo uint16` -- whose
// parseTypeLine SymAdd-overwrote bare `Foo` with uint16. Outer's method
// signature parsed next captured the uint16 Type in its Returns slice. Phase-2
// field access on the returned value then failed `undefined: <field>` because
// the receiver's Type was uint16, not the struct. Fixed by processing imports
// in a pre-pass before preRegisterTypes (goparser/import.go:importSrc).
func TestRemoteTransitiveImportBareKeyClobber(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":   "module example.com/x/inner\n",
				"inner.go": "package inner\n\ntype Foo uint16\n",
			},
		},
		remoteModule{
			path:    "example.com/x/outer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/outer\n",
				// Order matters: the method declaration that returns the bare
				// name `Foo` must come BEFORE the `type Foo struct` in source
				// order, so Phase-1 signature parsing captures whatever bare
				// `Foo` is at that moment. (With the pre-fix bare-key clobber,
				// imports ran in-loop and clobbered before the method's sig
				// parse; with the fix, imports run first, then preRegisterTypes
				// re-establishes the struct placeholder for outer.Foo.)
				"a_method.go": `package outer

type Bar struct{}

func (b Bar) Make() (Foo, int) { return Foo{val: 7}, 0 }
`,
				"b_type.go": `package outer

import "example.com/x/inner"

type Foo struct {
	dummy inner.Foo
	val   int
}

func Use() int {
	var b Bar
	f, _ := b.Make()
	return f.val
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("test", `import "example.com/x/outer"; println(outer.Use())`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteTargetBareAliasShadowsImportLocal: compiling a target package aliases
// its exports to bare keys (comp.aliasTargetTopLevel); a later imported package's
// unqualified reference to its OWN same-named type must resolve to that local type,
// not the leaked bare alias. Mirrors protobuf protocmp's `type reflectMessage
// Message` binding to proto.Message instead of protocmp's local Message. Fixed in
// goparser symGet (foreignBareAlias).
func TestRemoteTargetBareAliasShadowsImportLocal(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/aliaser",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/aliaser\n",
				// Compiled as the target: aliasTargetTopLevel exposes Shared at the
				// bare key "Shared" (an interface).
				"aliaser.go": "package aliaser\n\ntype Shared interface{ Mark() }\n",
			},
		},
		remoteModule{
			path:    "example.com/victim",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/victim\n",
				// a_wrap.go sorts first, so `type Wrap Shared` is parsed before
				// b_shared.go registers the local Shared -- the window in which the
				// leaked bare alias would otherwise win. len() forces a map, so a
				// resolution to the interface alias fails to compile.
				"a_wrap.go": `package victim

type Wrap Shared

func Use() int { w := Wrap{"a": 1, "b": 2}; return len(w) }
`,
				"b_shared.go": "package victim\n\ntype Shared map[string]int\n",
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	// Compile the target package first so its exports leak to bare keys.
	if _, err := i.Eval("example.com/aliaser", ""); err != nil {
		t.Fatalf("Eval aliaser: %v", err)
	}
	if _, err := i.Eval("test", `import "example.com/victim"; println(victim.Use())`); err != nil {
		t.Fatalf("Eval victim: %v", err)
	}
	if got, want := stdout.String(), "2\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemotePkgAliasCollision: two imported packages with the same directory
// name (-> same default short alias `lang`) live at different paths. Main
// imports both; the LAST main import to be processed SymSets bare `lang` to its
// own Pkg Symbol. The OUTER pkg has its own `import "<inner>"` (same default
// alias `lang`) and a Phase-2 deferred body that uses `lang.X{}`. Without the
// per-pkg Pkg-alias fix, Phase-2 lookup of `lang` finds whichever Pkg was last
// SymSet at the bare key (the wrong one), so `lang.X` resolves to the wrong
// type and `x.<field>` later fails "undefined: <field>". Fixed by also
// registering Pkg aliases at `<importingPkg>.<localAlias>` and routing Ident
// rewrites through that qualified key in Phase 2 (goparser/{decl.go,expr.go}).
func TestRemotePkgAliasCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/lang", // outer "lang" pkg.
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/lang\n",
				"lang.go": `package lang

import "example.com/x/inner/lang"

// X here is the OUTER lang.X (a wrapper).
type X struct {
	inner lang.X
}

// Use the inner lang's X composite literal inside a method body so the
// resolution happens in Phase 2 (deferred body), after main has finished
// running its own imports and overwritten the bare ` + "`lang`" + ` key.
func Wrap(v int) X { return X{inner: lang.X{V: v}} }

func (x X) Get() int { return x.inner.V }
`,
			},
		},
		remoteModule{
			path:    "example.com/x/inner/lang", // inner "lang" pkg (same short name as outer).
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner/lang\n",
				"lang.go": `package lang

type X struct{ V int }
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	// Main imports both outer and inner. Both have short name `lang`; the
	// LAST SymSet wins for the bare key. Without the per-pkg alias fix, Phase-2
	// deferred body of outer.Wrap would resolve `lang.X` via the wrong Pkg
	// (whichever main imported last). Use a renamed alias for the inner here so
	// main resolves outer.lang.Wrap unambiguously; the bug we're testing is
	// about main's `import "example.com/x/inner/lang"` SymSet, which uses the
	// default short name `lang` and thus overwrites bare `lang` after outer's
	// own parseSrc finished.
	src := `import inner "example.com/x/inner/lang"; import "example.com/x/lang"; println(lang.Wrap(42).Get(), inner.X{V: 3}.V)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "42 3\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteXTextCrash imports golang.org/x/text/language over the live proxy.
// It used to fault inside vm.patchRtype (a read-only static rtype was patched
// when the bare type names "Script"/"Region" collided between
// internal/language's `type Script uint16` and language's `type Script
// struct`); that crash is fixed -- see TestRemoteTypeNameCollision for a
// hermetic regression test. This case still fails on other unimplemented
// parser features in x/text (currently "undefined: LangID"), so it stays
// skipped; it also needs network access. Un-skip if/when x/text parses cleanly.
// TestRemotePkgQualifiedConstUnsetAddr exercises the pkg-qualified-const
// lookup path: a Const placeholder pre-registered by the parser with
// Index=UnsetAddr (-65535) was emitted as `GetGlobal -65535`, panicking
// at runtime with "index out of range [-65535]".  This was the second-to-last
// blocker on the x/text dual-import scenario; see comp/compiler.go:case
// lang.Period for the on-demand Data-slot allocation that fixes it.
func TestRemotePkgQualifiedConstUnsetAddr(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner\n",
				"inner.go": `package inner

type AliasType int8

const (
	Deprecated AliasType = iota
	Macro
	Legacy
)
`,
			},
		},
		remoteModule{
			path:    "example.com/x/outer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/outer\n",
				"outer.go": `package outer

import "example.com/x/inner"

func Classify(t inner.AliasType) string {
	switch t {
	case inner.Legacy:
		return "legacy"
	case inner.Macro:
		return "macro"
	}
	return "other"
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	src := `import (
		"example.com/x/inner"
		"example.com/x/outer"
	)
	outer.Classify(inner.Legacy)`
	v, err := i.Eval("test", src)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got := v.Interface(); got != "legacy" {
		t.Fatalf("Classify(inner.Legacy) = %v, want %q", got, "legacy")
	}
}

// TestRemoteTestTargetImportingPkg exercises the `mvm test <importpath>`
// load path (i.Eval with src=="" and a pkg-path target): without the
// importingPkg fix in comp.Compile, the target's own top-level symbols
// land at bare keys instead of `<pkg>.<name>` and lookups from the target's
// own deferred bodies fail with ErrUndefined. The fixture also embeds a
// lowercase struct so we cover the matching FieldLookup fallback for
// promoted fields through unexported embedded types (which reflect.FieldByName
// does NOT resolve for value-embedded unexported names).
func TestRemoteTestTargetImportingPkg(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/target",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/target\n",
			"target.go": `package target

type hidden struct {
	X int
}

type Outer struct {
	hidden
}

func (o *Outer) Get() int {
	return o.X
}

func Make() *Outer { return &Outer{hidden: hidden{X: 42}} }
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	// Direct-target load (mirrors main.go testCmd's `i.Eval(target, "")`).
	// Pre-fix this failed with `undefined: X` from Outer.Get's body because:
	//   1. importingPkg wasn't set, so the target's `hidden` type and Outer
	//      methods landed at bare keys.
	//   2. reflect.FieldByName doesn't promote through value-embedded unexported
	//      `hidden`, and the compiler had no mvm-level FieldLookup fallback.
	// With both fixes the load succeeds.
	i.SetIncludeTests(true)
	if _, err := i.Eval("example.com/x/target", ""); err != nil {
		t.Fatalf("loading target: %v", err)
	}
}

// TestRemoteTestTargetForwardVarType covers the uuid `Nil UUID` case: a typed
// var whose type lives in a sibling file parsed later. Also asserts the test
// driver can find Test* by short name (aliasTargetTopLevel). See
// [[project_uuid_test_regression]].
func TestRemoteTestTargetForwardVarType(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/fwdvar",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/fwdvar\n",
			// "z_user.go" (z) is parsed after "fwdvar.go" but UUID's definition is
			// in "fwdvar.go" -- ordering matches the uuid pkg's hash.go/uuid.go.
			"fwdvar.go": `package fwdvar

type UUID [16]byte
`,
			"z_user.go": `package fwdvar

var (
	NameSpaceX = UUID{0xff}
	Nil        UUID
)

type Holder struct {
	UUID  UUID
	Valid bool
}

func (h *Holder) Reset() {
	h.UUID, h.Valid = Nil, false
}
`,
			"z_user_test.go": `package fwdvar

import "testing"

func TestReset(t *testing.T) {
	var h Holder
	h.Valid = true
	h.Reset()
	if h.Valid {
		t.Fatal("Reset should clear Valid")
	}
}
`,
		},
	})

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.SetIncludeTests(true)
	if _, err := i.Eval("example.com/x/fwdvar", ""); err != nil {
		t.Fatalf("loading fwdvar: %v\nstderr: %s", err, stderr.String())
	}
	// Also exercise the test-driver alias path: FuncNames must find TestReset.
	names := i.FuncNames("Test")
	if len(names) == 0 {
		t.Fatalf("FuncNames found no Test* funcs; expected at least TestReset")
	}
}

// TestRemoteIfaceDispatchSignatureCollision: a user-defined Elem(int) string
// must not shadow reflect.Type.Elem() reflect.Type during interface dispatch.
// See [[project_iface_dispatch_signature_collision]].
func TestRemoteIfaceDispatchSignatureCollision(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/ifacecollide",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/ifacecollide\n",
			"ifacecollide.go": `package ifacecollide

import "reflect"

// Index is a string-typed defined type with its own Elem method whose
// signature (func(int) string) differs from reflect.Type.Elem (func() reflect.Type).
// Pre-fix, findConcreteFuncSym would pick Index.Elem when resolving
// reflect.Type.Elem on an interface receiver.
type Index string

func (i Index) Elem(_ int) string { return string(i) }

type S struct {
	X *int
}

// Probe chains reflect.Type.Elem().Kind() -- the cldr-style chain that
// breaks when Elem is signature-mismatched with Index.Elem.
func Probe() reflect.Kind {
	t := reflect.TypeOf(S{})
	f := t.Field(0)
	return f.Type.Elem().Kind()
}

var _ = Index("seed")
`,
		},
	})

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("example.com/x/ifacecollide", ""); err != nil {
		t.Fatalf("loading ifacecollide: %v\nstderr: %s", err, stderr.String())
	}
}

// TestRemotePromotedFieldAndMethodThroughAnon mirrors cldr's shape: a `rule`
// type with methods embedded in an anonymous wrapper (with another field, so
// reflect.StructOf can't set Anonymous because rule has methods), exposed via
// a promoted slice. Exercises FieldLookup's promoted-field chain and
// promotedMethod's cross-pkg method lookup. See [[project_promoted_field_method_anon]].
func TestRemotePromotedFieldAndMethodThroughAnon(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/cldrlike",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/cldrlike\n",
			"cldrlike.go": `package cldrlike

type rule struct {
	Value  string
	Before string
}

func (r *rule) Read() string { return r.Before }

type wrap struct {
	Name string
	rule
}

type Holder struct {
	Rules struct {
		Items []*wrap
	}
}

func (h Holder) Process() string {
	var out string
	for _, r := range h.Rules.Items {
		out += r.Before + ":" + r.Read() + ";"
	}
	return out
}

func New() Holder {
	return Holder{Rules: struct{ Items []*wrap }{Items: []*wrap{{Name: "x", rule: rule{Before: "p"}}}}}
}
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("example.com/x/cldrlike", ""); err != nil {
		t.Fatalf("loading cldrlike: %v", err)
	}
}

// TestRemoteReflectTypeForCrossPkg covers reflect.TypeFor[T]() where T is a
// type defined in a different (and still-being-compiled) package. The
// interpreted-source generic shim registered by stdlib/reflect_shim.go has
// pkgPath="reflect", so re-parsing its body sets CompilingPkg="reflect" --
// under which the substituted bare type-arg name (here "MyKind") cannot be
// resolved through symGet's CompilingPkg fallback. Without the temp
// type-arg install in goparser/expr.go's emitGenericFunc, parseTypeExpr on
// `*MyKind` fails and the parser mis-emits Deref instead of building a
// pointer-type expression. Verifies the cross-pkg path that the in-process
// etest cases (which use main-pkg types aliased to bare keys) cannot reach.
func TestRemoteReflectTypeForCrossPkg(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/typefortarget",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/typefortarget\n",
			"types.go": `package typefortarget

import "reflect"

type MyKind struct {
	A uint64
	B uint16
}

// Calling reflect.TypeFor[MyKind]() from a non-main package exercises the
// substituted-T-in-shim-body resolution path: T -> "MyKind" with
// CompilingPkg="reflect" while the actual type lives at
// "example.com/x/typefortarget.MyKind".
func Size() uintptr {
	return reflect.TypeFor[MyKind]().Size()
}
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/typefortarget"; println(typefortarget.Size())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	// MyKind layout: uint64 (8) + uint16 (2) + 6-byte tail padding = 16.
	if got, want := stdout.String(), "16\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemotePlaceholderRtypeGrow checks that a package-level zero-value of an
// imported struct type is fully zero-initialized even when the struct's rtype
// grew via SetFields after the var symbol's Value was first allocated.
//
// Trigger shape: an imported package has `type Tag struct{...}` (a String()
// method makes mvm bridge the type via fmt.Stringer) and `var Und Tag`. Before
// the fix, parseTypeVar's call to vm.NewValue(Tag.Rtype) ran while Tag's rtype
// was still the 8-byte placeholder; later SetFields patched the rtype to 32
// bytes but Und's slot stayed 8 bytes backed. Reads past offset 7 hit adjacent
// heap memory, so Und.LastField was garbage. Repro of the language.Und bug
// that mangled x/text/internal/language's TestBuilder.
func TestRemotePlaceholderRtypeGrow(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/lang",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/lang\n",
			"lang.go": `package lang

type Tag struct {
	A uint16
	B uint16
	C uint16
	D byte
	E uint16
	S string
}

func (t Tag) String() string { return t.S }

var Und Tag
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/lang"
println(lang.Und.A, lang.Und.B, lang.Und.C, lang.Und.D, lang.Und.E, len(lang.Und.S))`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "0 0 0 0 0 0\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteZeroInitLocalShortNameCollision triggers the language.Matcher.Match
// panic from project_matcher_match_placeholder_mismatch: `var v <pkg>.<Type>`
// inside a function body used to zero-init v with a sibling pkg's same-named
// type whose rtype differed from v's slot. zeroInitLocals resolved the type
// by short name and re-qualified through CompilingPkg, finding the host pkg's
// "Tag" instead of the imported pkg's "Tag" with the slot's actual rtype.
// reflect.Set then panicked at SetLocal with mismatched struct rtypes.
func TestRemoteZeroInitLocalShortNameCollision(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/dual",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/dual\n",
			"inner/inner.go": `package inner

type Tag struct {
	A uint16
	B uint16
	C uint16
	D byte
	E uint16
	S string
}
`,
			"outer.go": `package dual

import "example.com/x/dual/inner"

type Tag struct {
	X uint64
	Y uint64
}

func F() inner.Tag {
	var v inner.Tag
	v.A = 7
	return v
}
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/dual"
println(dual.F().A)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteMethodScopedLocalCollidesAcrossPkgs reproduces the
// x/text/language TestMarshal failure. Two packages each define a method
// with the same bare name (e.g. `(*Tag).Do`); each method's body declares
// a `:=` local. mvm keys local symbols by funcScope (bare function name),
// so the second package's parse finds the first's stale LocalVar for the
// same local-name and reuses it -- without re-incrementing framelen. The
// function-entry Grow then under-reserves frame slots, and subsequent stack
// pushes corrupt the just-declared local.
//
// Concretely in the outer method below: after `err := <pkg-call>`, the
// next assignment to *t issues a HeapGet that pushes at err's storage,
// overwriting err with a pointer to the Tag receiver. The caller observes
// a non-nil "error" instead of nil.
func TestRemoteMethodScopedLocalCollidesAcrossPkgs(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/outer/pkg",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/outer/pkg\n",
				"pkg.go": `package pkg

import inner "example.com/inner/pkg"

type Tag struct {
	A int
	B int
}

// Mirrors language.Tag.UnmarshalText: var of imported same-name pkg's
// type, ':=' to capture its method's return, then '*t = ...'.
func (t *Tag) Do(text []byte) error {
	var x inner.Tag
	err := x.Do(text)
	*t = Tag{A: 1, B: 2}
	_ = x
	return err
}
`,
			},
		},
		remoteModule{
			path:    "example.com/inner/pkg",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/inner/pkg\n",
				"pkg.go": `package pkg

type Tag struct {
	A int
	B int
}

// Body must register a LocalVar named err at a higher slot index than
// the outer package's same-named method will allocate. The extra a
// pushes err to slot Index=2 -- without the fix the outer reuses that
// Index but its function-entry Grow only reserved 1 slot, leaving err
// in stack space that the next push clobbers.
func (t *Tag) Do(text []byte) error {
	var a int
	err := set(t, 134)
	a = a + 1
	_ = a
	return err
}

func set(t *Tag, v int) error {
	t.A = v
	t.B = v
	return nil
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/outer/pkg"
var tag pkg.Tag
err := tag.Do([]byte("en"))
println(err == nil)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "true\n"; got != want {
		t.Errorf("stdout: got %q want %q", got, want)
	}
}

// TestRemoteCrossPkgSameFuncLocalsCollision: two pkgs with the same top-level
// func name share p.funcScope, so inner's named-return LocalVar at scoped key
// `<name>/err` survives into outer's parse. Pre-fix, addOrRebindLocalVar rebound
// outer's `err` to inner's Symbol, leaving outer's `l` and `err` aliased on
// the same frame slot and reflect-Set'ing a wrongly-typed Value into the
// composite's uint16 field. Mirrors x/text/language's `ParseBase` wrapping
// `internal/language.ParseBase`.
func TestRemoteCrossPkgSameFuncLocalsCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner/diff",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner/diff\n",
				"lang.go": `package diff

type Language uint16

func ParseBase(s string) (l Language, err error) {
	if len(s) == 2 && s[0] == 'e' && s[1] == 'n' {
		return Language(313), nil
	}
	return 0, nil
}
`,
			},
		},
		remoteModule{
			path:    "example.com/x/lang",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/lang\n",
				"lang.go": `package lang

import "example.com/x/inner/diff"

type Base struct {
	langID diff.Language
}

func ParseBase(s string) (Base, error) {
	l, err := diff.ParseBase(s)
	return Base{l}, err
}

func (b Base) ID() uint16 { return uint16(b.langID) }
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/lang"
b, _ := lang.ParseBase("en")
println(b.ID())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "313\n"; got != want {
		t.Errorf("stdout: got %q want %q", got, want)
	}
}

// TestRemoteXTextLanguageImport is a smoke test that x/text/language --
// historically a deep stress on cross-pkg type resolution, generic-shim
// dispatch, and reflect-via-mvm plumbing -- imports end-to-end through
// the default Go module proxy. Originally tracked vm.patchRtype crashes;
// after the Phase-2 path-B refactor + reflect.TypeFor shim + named-return
// slice/map zero-init fixes it now compiles cleanly.
//
// Skipped in -short mode because it requires network access to
// proxy.golang.org and the (cached) module zip.
func TestRemoteXTextLanguageImport(t *testing.T) {
	if testing.Short() {
		t.Skip("requires network access to the Go module proxy")
	}
	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{}))
	if _, err := i.Eval("test", `import "golang.org/x/text/language"; println(language.English.String())`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "en\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func buildZip(t *testing.T, mod, ver string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := mod + "@" + ver + "/"
	for name, body := range files {
		w, err := zw.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestRemoteImportedInitBeforeMainVarInit asserts Go init order across packages:
// an imported package's init() runs before the importer's var inits (dep.init
// sets dep.Ready, read by main's `var got = dep.Ready`). Also guards that main's
// own init() still runs after main's var inits (within-package order).
func TestRemoteImportedInitBeforeMainVarInit(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/dep",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/dep\n",
				"dep.go": `package dep

var Ready string

func init() { Ready = "INITIALIZED" }
`,
			},
		},
	)

	src := `package main

import "example.com/dep"

// Reads an imported package's init() side effect at var-init time.
var got = dep.Ready

// main's own var init; main's init() must observe it (within-package order).
var local = "set"
var localSeenByInit string

func init() { localSeenByInit = local }

func main() {
	println(got)
	println(localSeenByInit)
}
`

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "INITIALIZED\nset\n"; got != want {
		t.Errorf("stdout: got %q, want %q (imported init must precede main var init; main init after main var init)", got, want)
	}
}

// TestRemoteGenericIfaceConstraintForeignArg mirrors proto.CloneOf: a generic
// func with an interface constraint in package "gen" is instantiated with a
// concrete *T from package "msg". msg's pkg-qualified method key made the
// short-name lookup miss it ("does not satisfy constraint"); MethodByName
// resolves it across packages.
func TestRemoteGenericIfaceConstraintForeignArg(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/gen",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/gen\n",
				"gen.go": `package gen

type Msg interface { Tag() int }

func Clone[M Msg](m M) M { return m }
`,
			},
		},
		remoteModule{
			path:    "example.com/msg",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/msg\n",
				"msg.go": `package msg

type T struct{ N int }

func (t *T) Tag() int { return t.N }

func New() *T { return &T{N: 7} }
`,
			},
		},
	)

	src := `package main

import (
	"example.com/gen"
	"example.com/msg"
)

func main() {
	got := gen.Clone(msg.New())
	println(got.Tag())
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
