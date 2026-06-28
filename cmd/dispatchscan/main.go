// Command dispatchscan reports each stdlib package's "dispatch surface": the
// exported APIs whose parameters carry an interface, which a native bridge must
// dispatch on a caller-supplied value. On wasm interpreted method entries are
// the -1 unreachable-method sentinel, so a native bridge dispatching an
// interpreted method crashes. A package is therefore safe to bridge only if its
// dispatch surface is empty: basic types and func callbacks (reflect.MakeFunc
// trampolines) are safe; named non-empty interfaces and reflected `any` are not.
//
// Usage: dispatchscan [-v] [pkg ...]   (default: the wasm-interpreted set)
//
// It is a signature-level over-approximation: it flags interface parameters
// without proving the bridge calls their methods, and does not recurse into
// struct fields or type parameters. An empty surface (SAFE) is sound; a
// non-empty one (RISKY) marks packages to confirm empirically or keep
// interpreted. See docs/modules/stubs.md.
package main

import (
	"flag"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"
)

// defaultPkgs is the wasm-interpreted candidate set plus the packages already
// bridged (strconv, unicode*) and a few keep-native ones for contrast.
var defaultPkgs = []string{
	"strconv", "unicode", "unicode/utf8", "unicode/utf16",
	"strings", "bytes", "sort", "bufio", "fmt", "errors",
	"io", "context", "flag", "regexp", "time",
	"container/heap", "container/list", "container/ring",
	"encoding/json", "encoding/binary", "encoding/hex", "encoding/base64",
	"math", "math/bits", "hash/crc32",
}

type siteKind int

const (
	kindIface siteKind = iota // named non-empty interface: hard dispatch risk
	kindAny                   // empty interface: dispatch only via reflection
	kindFunc                  // func callback: safe (MakeFunc trampoline)
)

func (k siteKind) String() string {
	switch k {
	case kindIface:
		return "iface"
	case kindAny:
		return "any"
	default:
		return "func"
	}
}

type site struct {
	symbol string // "Sort" or "Buffer.ReadFrom"
	param  string // "sort.Interface", "io.Reader", "any", "func"
	kind   siteKind
}

type result struct {
	pkg   string
	apis  int // exported funcs + methods scanned
	sites []site
}

func main() {
	verbose := flag.Bool("v", false, "list every site, including any/func (safe) ones")
	flag.Parse()
	pkgs := flag.Args()
	if len(pkgs) == 0 {
		pkgs = defaultPkgs
	}
	imp := importer.ForCompiler(token.NewFileSet(), "source", nil)
	exit := 0
	var safe, risky []string
	for _, p := range pkgs {
		r, err := analyze(imp, p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%-22s ERROR  %v\n", p, err)
			exit = 1
			continue
		}
		fmt.Print(r.format(*verbose))
		if r.ifaceCount() == 0 && r.anyCount() == 0 {
			safe = append(safe, p)
		} else {
			risky = append(risky, p)
		}
	}
	fmt.Printf("\nSAFE to bridge (empty dispatch surface): %s\n", strings.Join(safe, " "))
	fmt.Printf("RISKY (keep interpreted / verify):       %s\n", strings.Join(risky, " "))
	os.Exit(exit)
}

func analyze(imp types.Importer, path string) (*result, error) {
	tpkg, err := imp.Import(path)
	if err != nil {
		return nil, err
	}
	r := &result{pkg: path}
	scope := tpkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.Func:
			if sig, ok := o.Type().(*types.Signature); ok {
				r.inspect(name, sig)
			}
		case *types.TypeName:
			named, ok := o.Type().(*types.Named)
			if !ok {
				continue
			}
			// The pointer method set covers value- and pointer-receiver methods.
			ms := types.NewMethodSet(types.NewPointer(named))
			for sel := range ms.Methods() {
				m := sel.Obj()
				fn, ok := m.(*types.Func)
				if !ok || !m.Exported() {
					continue
				}
				if sig, ok := fn.Type().(*types.Signature); ok {
					r.inspect(name+"."+m.Name(), sig)
				}
			}
		}
	}
	sort.Slice(r.sites, func(i, j int) bool {
		if r.sites[i].kind != r.sites[j].kind {
			return r.sites[i].kind < r.sites[j].kind
		}
		return r.sites[i].symbol < r.sites[j].symbol
	})
	return r, nil
}

func (r *result) inspect(symbol string, sig *types.Signature) {
	r.apis++
	params := sig.Params()
	for i := range params.Len() {
		pt := params.At(i).Type()
		if sig.Variadic() && i == params.Len()-1 {
			if s, ok := pt.(*types.Slice); ok {
				pt = s.Elem() // ...T is []T; classify the element
			}
		}
		if name, kind, ok := ifaceIn(pt, 0); ok {
			r.sites = append(r.sites, site{symbol, name, kind})
		}
	}
}

// ifaceIn reports the first interface or func reachable in a parameter type,
// unwrapping pointers and one element of slice/array/chan/map. It does not
// recurse into struct fields or type parameters.
func ifaceIn(t types.Type, depth int) (string, siteKind, bool) {
	if depth > 6 {
		return "", 0, false
	}
	t = types.Unalias(t) // `any` and other aliases resolve to their target
	switch u := t.(type) {
	case *types.Named:
		if it, ok := u.Underlying().(*types.Interface); ok {
			if it.NumMethods() == 0 {
				return t.String(), kindAny, true
			}
			return t.String(), kindIface, true
		}
		return "", 0, false
	case *types.Interface:
		if u.NumMethods() == 0 {
			return "any", kindAny, true
		}
		return t.String(), kindIface, true
	case *types.Signature:
		return "func", kindFunc, true
	case *types.Pointer:
		return ifaceIn(u.Elem(), depth+1)
	case *types.Slice:
		return ifaceIn(u.Elem(), depth+1)
	case *types.Array:
		return ifaceIn(u.Elem(), depth+1)
	case *types.Chan:
		return ifaceIn(u.Elem(), depth+1)
	case *types.Map:
		if n, k, ok := ifaceIn(u.Elem(), depth+1); ok {
			return n, k, ok
		}
		return ifaceIn(u.Key(), depth+1)
	}
	return "", 0, false
}

func (r *result) ifaceCount() int { return r.count(kindIface) }
func (r *result) anyCount() int   { return r.count(kindAny) }

func (r *result) count(k siteKind) int {
	n := 0
	for _, s := range r.sites {
		if s.kind == k {
			n++
		}
	}
	return n
}

func (r *result) format(verbose bool) string {
	iface, anys, funcs := r.count(kindIface), r.count(kindAny), r.count(kindFunc)
	verdict := "SAFE"
	switch {
	case iface > 0:
		verdict = "RISKY-iface"
	case anys > 0:
		verdict = "RISKY-any"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-20s %-12s apis=%-4d iface=%-3d any=%-3d func=%-3d\n",
		r.pkg, verdict, r.apis, iface, anys, funcs)
	for _, s := range r.sites {
		if !verbose && s.kind != kindIface {
			continue // default view: only the hard dispatch sites
		}
		fmt.Fprintf(&b, "    %-24s %-22s (%s)\n", s.symbol, s.param, s.kind)
	}
	return b.String()
}
