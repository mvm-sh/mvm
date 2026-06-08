package goparser

import (
	"fmt"
	"maps"
	"sync"

	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// genericShim is one registered interpreted-source generic shim attached to
// an otherwise-native package. Used when a Go stdlib generic cannot be
// expressed via a single reflect.ValueOf binding (e.g. reflect.TypeFor): the
// shim provides an interpreted definition equivalent to the upstream one,
// parsed against the native package's context so `pkg.Sym[T](...)` resolves
// through the normal generic-instantiation pipeline.
type genericShim struct {
	source     string   // entire shim source ("package <pkg>" + decl(s)).
	nativeRefs []string // names from p.Packages[pkg].Values referenced bare by the source.
}

var (
	shimMu       sync.Mutex
	shimRegistry = map[string][]genericShim{}
)

// RegisterGenericShim queues an interpreted-source shim to install into pkg's
// symbol table the next time a Parser populates that package (typically from
// ImportPackageValues). source must start with `package <pkg>`. nativeRefs
// lists names referenced bare by source from pkg's exports: those are
// pre-seeded into p.Symbols at qualified keys so symGet's importingPkg /
// CompilingPkg fallback resolves them at signature parse and body
// instantiation time.
//
// Safe to call from package init; the registry persists for the process
// lifetime and is consulted by every parser created thereafter.
func RegisterGenericShim(pkg, source string, nativeRefs []string) {
	shimMu.Lock()
	defer shimMu.Unlock()
	shimRegistry[pkg] = append(shimRegistry[pkg], genericShim{source: source, nativeRefs: nativeRefs})
}

// installGenericShims parses every registered shim against its target native
// package's context, registering the resulting generic templates at the
// canonical qualified keys (e.g. "reflect.TypeFor"). Skipped for packages
// without registered shims or absent from p.Packages.
func (p *Parser) installGenericShims() error {
	shimMu.Lock()
	snapshot := make(map[string][]genericShim, len(shimRegistry))
	maps.Copy(snapshot, shimRegistry)
	shimMu.Unlock()

	for pkgPath, shims := range snapshot {
		pkg, ok := p.Packages[pkgPath]
		if !ok {
			continue
		}
		for _, sh := range shims {
			if err := p.installOneShim(pkgPath, pkg, sh); err != nil {
				return fmt.Errorf("installing shim for %s: %w", pkgPath, err)
			}
		}
	}
	return nil
}

func (p *Parser) installOneShim(pkgPath string, nativePkg *symbol.Package, sh genericShim) error {
	// Pre-seed each declared native reference under its qualified key
	// ("<pkgPath>.<name>") so the shim's signature parse (at registration
	// time) and its instantiated body parse (at call time) resolve bare
	// idents through symGet's importingPkg / CompilingPkg fallback rather
	// than through the comp-time Period handler that we never visit for
	// shim-internal references.
	for _, name := range sh.nativeRefs {
		key := QualifyName(pkgPath, name)
		if _, exists := p.Symbols[key]; exists {
			continue
		}
		v, ok := nativePkg.Values[name]
		if !ok {
			return fmt.Errorf("native reference %q.%q not exported", pkgPath, name)
		}
		sym := &symbol.Symbol{
			Name:    name,
			Index:   symbol.UnsetAddr,
			PkgPath: pkgPath,
			Used:    true,
		}
		if rtype, ok := v.UnwrapType(); ok {
			sym.Kind = symbol.Type
			sym.Value = vm.NewValue(rtype)
			sym.Type = &vm.Type{Name: rtype.Name(), Rtype: rtype}
		} else {
			rt := v.Type()
			sym.Kind = symbol.Value
			sym.Value = v
			sym.Type = &vm.Type{Name: rt.Name(), Rtype: rt}
		}
		p.SymSet(key, sym)
	}

	// Parse the shim against the target package's context. importingPkg
	// routes top-level Func/Generic registration to the canonical
	// "<pkgPath>.<name>" key (Phase 2 Path B). pkgName is saved/restored so
	// `package <pkgPath>` inside the shim does not conflict with whatever
	// the outer parser was midway through. PosBase is also restored so
	// later tokens lacking explicit positions (e.g. compiler-synthesized
	// emits) do not get diagnostic-attributed to the shim source.
	savedPkgName := p.pkgName
	savedImportingPkg := p.importingPkg
	savedPosBase := p.PosBase
	p.pkgName = ""
	p.importingPkg = pkgPath
	defer func() {
		p.pkgName = savedPkgName
		p.importingPkg = savedImportingPkg
		p.PosBase = savedPosBase
	}()

	_, err := p.ParseAll(pkgPath+"/<shim>", sh.source)
	return err
}
