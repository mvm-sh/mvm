package interp

import (
	"sort"
	"strings"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/symbol"
)

// autoImportPreferred maps an ambiguous short package name to the canonical
// import path to use when multiple registered stdlib packages share the same
// basename. These are the only four collisions in the current stdlib set.
var autoImportPreferred = map[string]string{
	"rand":     "math/rand",     // vs crypto/rand, math/rand/v2
	"template": "text/template", // vs html/template
	"scanner":  "text/scanner",  // vs go/scanner
	"pprof":    "runtime/pprof", // vs net/http/pprof
}

// AutoImportPackages registers every package already loaded in i.Packages
// under its short name, so that REPL or -e users can reference them (e.g.
// time.Now()) without writing an explicit import statement.
//
// Call it after ImportPackageValues and before Eval/Repl. Explicit import
// statements parsed later will cleanly overwrite the pre-registered symbol.
func (i *Interp) AutoImportPackages() {
	groups := map[string][]string{}
	for importPath := range i.Packages {
		name := goparser.PackageName(importPath)
		groups[name] = append(groups[name], importPath)
	}
	for name, paths := range groups {
		chosen := pickPreferredPath(name, paths)
		i.SymSet(name, &symbol.Symbol{
			Kind:    symbol.Pkg,
			PkgPath: chosen,
			Index:   symbol.UnsetAddr,
			Name:    name,
		})
	}
}

// pickPreferredPath resolves which import path to bind to a short name when
// several packages share it. The preference table wins; otherwise we pick
// the path with the fewest segments, breaking ties lexically.
func pickPreferredPath(name string, paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	if p, ok := autoImportPreferred[name]; ok {
		for _, cand := range paths {
			if cand == p {
				return p
			}
		}
	}
	sort.Slice(paths, func(a, b int) bool {
		sa := strings.Count(paths[a], "/")
		sb := strings.Count(paths[b], "/")
		if sa != sb {
			return sa < sb
		}
		return paths[a] < paths[b]
	})
	return paths[0]
}
