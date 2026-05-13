package goparser

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"unicode"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// PackageSource is a single .go file's basename and content as loaded by
// LoadPackageSources.
type PackageSource struct {
	Name    string // basename (e.g. "uuid.go")
	Content string
}

// LoadPackageSources returns the .go files of the given package import path
// (a directory in the FS chain pkgfs -> stdlibfs -> remotefs), filtered by
// build tags (file-name and //go:build directives). When includeTests is
// false, _test.go files are skipped (matching `import "X"` resolution);
// pass true to include them (used by `mvm test <importpath>`).
//
// Result order matches fs.ReadDir, which is sorted by filename.
func (p *Parser) LoadPackageSources(importPath string, includeTests bool) ([]PackageSource, error) {
	if p.pkgfs == nil {
		p.pkgfs = os.DirFS(".")
	}
	fsys := p.pkgfs
	fi, err := fs.Stat(fsys, importPath)
	for _, fb := range []fs.FS{p.stdlibfs, p.remotefs} {
		if err == nil || fb == nil {
			break
		}
		if fi2, err2 := fs.Stat(fb, importPath); err2 == nil {
			fsys, fi, err = fb, fi2, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a package directory", importPath)
	}
	entries, err := fs.ReadDir(fsys, importPath)
	if err != nil {
		return nil, err
	}
	var out []PackageSource
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if !MatchFileName(e.Name(), p.buildCtx) {
			continue
		}
		buf, err := fs.ReadFile(fsys, importPath+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		src := string(buf)
		if !matchBuildDirective(src, p.buildCtx) {
			continue
		}
		out = append(out, PackageSource{Name: e.Name(), Content: src})
	}
	// When loading tests, filter out external _test.go files (those declaring
	// `package X_test` instead of `package X`). Go's testing tool compiles
	// them as a separate package; mvm does not support that yet, and feeding
	// them into the same parser scope would mis-resolve qualified imports
	// (e.g. `errors.WithMessage` where `errors` aliases the package under test).
	if includeTests {
		names := make([]string, len(out))
		var mainPkg string
		for i, s := range out {
			names[i] = extractPackageName(s.Content)
			if mainPkg == "" && names[i] != "" && !strings.HasSuffix(s.Name, "_test.go") {
				mainPkg = names[i]
			}
		}
		if mainPkg != "" {
			filtered := out[:0]
			for i, s := range out {
				if !strings.HasSuffix(s.Name, "_test.go") || names[i] == mainPkg {
					filtered = append(filtered, s)
				}
			}
			out = filtered
		}
	}
	return out, nil
}

func extractPackageName(src string) string {
	// skipped; block comments before `package` are not handled (Go style places
	// `package` directly after build tags, so this is sufficient in practice).
	for src != "" {
		var line string
		if i := strings.IndexByte(src, '\n'); i >= 0 {
			line, src = src[:i], src[i+1:]
		} else {
			line, src = src, ""
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "package ")
		if !ok {
			return ""
		}
		rest = strings.TrimSpace(rest)
		end := len(rest)
		for i, r := range rest {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
				end = i
				break
			}
		}
		return rest[:end]
	}
	return ""
}

func (p *Parser) importSrc(pkgPath string) (err error) {
	// Save and restore parser state so the imported package's
	// "package" declaration does not conflict with the current one,
	// and so includeTests stays local to the top-level test target
	// rather than leaking into transitive imports.
	savedPkgName := p.pkgName
	savedIncludeTests := p.includeTests
	savedImportingPkg := p.importingPkg
	p.pkgName = ""
	p.includeTests = false
	p.importingPkg = pkgPath
	defer func() {
		p.pkgName = savedPkgName
		p.includeTests = savedIncludeTests
		p.importingPkg = savedImportingPkg
	}()

	// Snapshot existing symbol pointers so we can identify bindings
	// added or replaced by this import. A later import that redefines an
	// exported name (e.g. `Equal` in both `maps` and `slices`) swaps the
	// pointer at p.Symbols[k]; key-only tracking would miss the rebind
	// and fail to create the qualified alias for the second package.
	existing := make(map[string]*symbol.Symbol, len(p.Symbols))
	for k, s := range p.Symbols {
		existing[k] = s
	}

	remaining, err := p.ParseAll(pkgPath, "")
	if err != nil {
		return fmt.Errorf("while importing %q: %w", pkgPath, err)
	}

	// Store remaining declarations (func bodies, var initializers)
	// for code generation by the outer ParseAll / Compile. They are already
	// tagged with their originating package path by ParseAll.
	p.importRemaining = append(p.importRemaining, remaining...)

	// Collect exported symbols into a Package entry (the package's public
	// surface) and create package-qualified aliases for *every* top-level
	// symbol this import added or replaced -- exported or not -- so that
	// Phase-2 code from this package can resolve its own names even after a
	// sibling import shadows a bare key in the symbol table (see DeferredDecl).
	pkg := &symbol.Package{
		Path:   pkgPath,
		Values: map[string]vm.Value{},
	}
	var newKeys, demoteKeys []string
	qualifiedPrefix := pkgPath + "."
	for k, s := range p.Symbols {
		if existing[k] == s {
			continue
		}
		// Type writers in Path B step 1 register at the canonical pkgKey form
		// "<pkgPath>.<name>" directly (see pkgKey, parseTypeLine,
		// preRegisterTypes, registerType). For those entries we still publish
		// the short-name in pkg.Values (for cross-pkg `pkg.Member` resolution
		// that falls back to `pkg.Values`), but we skip the qualified-alias
		// SymSet below: aliasing again would yield "<pkgPath>.<pkgPath>.<name>".
		// Checked BEFORE isScopedKey because the qualified key contains '/'
		// inside its pkg-path prefix.
		if strings.HasPrefix(k, qualifiedPrefix) {
			short := k[len(qualifiedPrefix):]
			if isScopedKey(short) {
				continue
			}
			if s.Kind != symbol.Generic && IsExported(short) {
				pkg.Values[short] = s.Value
			}
			continue
		}
		// Skip scoped keys (param placeholders, locals) AND foreign pkg's
		// qualified aliases ("other.pkg/path.name" registered by a transitive
		// import); only top-level names registered by THIS pkg need aliasing.
		if isScopedKey(k) {
			continue
		}
		newKeys = append(newKeys, k)
		if s.Kind != symbol.Generic && IsExported(k) {
			pkg.Values[k] = s.Value
		}
		// A package-level var's symbol is mutated in place when its initializer
		// is compiled in Phase 2 (its type is inferred from the initializer). If
		// another import later declares the same bare name, that mutation would
		// clobber this package's symbol -- and the package-qualified alias would
		// then point at the clobbered object. Drop the bare key so each package's
		// var symbol stays distinct; Phase 2 resolves the name via CompilingPkg
		// (symGet / comp.symAt) and `pkg.Member` accesses via the qualified
		// alias. (Consts are kept: resolved in Phase 1, never mutated, and
		// appear in type expressions like array lengths that are looked up by
		// bare name.)
		if s.Kind == symbol.Var && existing[k] == nil {
			demoteKeys = append(demoteKeys, k)
		}
	}
	// Create qualified aliases after the loop to avoid mutating p.Symbols during iteration.
	for _, k := range newKeys {
		s := p.Symbols[k]
		// A type's Symbol is mutated in place when a sibling import re-declares
		// the same bare name (parseTypeLine reuses the existing symbol and resets
		// its .Type). Alias to a shallow copy so this package's qualified entry
		// keeps pointing at the right *vm.Type; everything else can share the
		// live symbol. (Demoting the bare type key like we do for vars is not an
		// option: many sites still look types up by bare name.)
		if s.Kind == symbol.Type {
			cp := *s
			s = &cp
		}
		p.Symbols[pkgPath+"."+k] = s
	}
	for _, k := range demoteKeys {
		delete(p.Symbols, k)
	}
	p.Packages[pkgPath] = pkg

	return nil
}

// ParseAll parses code and its dependencies, and returns the still-to-be-
// code-generated declarations (func bodies, var initializers), each tagged
// with its originating package path, or an error. When src == "" the source
// is loaded from the package directory `name`, so its decls are tagged with
// `name`; otherwise (main package / REPL) they are tagged with "".
func (p *Parser) ParseAll(name, src string) (out []DeferredDecl, err error) {
	var decls []Tokens

	if src == "" {
		sources, err := p.LoadPackageSources(name, p.includeTests)
		if err != nil {
			return out, err
		}
		for _, s := range sources {
			p.PosBase = p.Sources.Add(name+"/"+s.Name, s.Content)
			d, derr := p.scanDecls(s.Content)
			if derr != nil {
				return out, derr
			}
			decls = append(decls, d...)
		}
	} else {
		p.PosBase = p.Sources.Add(name, src)
		decls, err = p.scanDecls(src)
		if err != nil {
			return out, err
		}
	}

	// Pre-register struct and interface type placeholders so that forward,
	// mutual, and self-references can resolve during parsing. Placeholders
	// land under this pkg's pkgKey ("<importingPkg>.<name>"), so a transitive
	// sub-import (e.g. language -> internal/language) writing its OWN
	// `type Foo uint16` at <innerPkg>.Foo doesn't collide. Placeholders are
	// untracked: they survive the retry loop cleanup.
	p.preRegisterTypes(decls)

	// Phase 1: resolve all declarations and expand generic methods in a
	// single fixed-point loop. Each pass (a) retries decls that failed with
	// ErrUndefined, then (b) emits any pending (instance x method) pair for
	// registered generic types. The loop terminates when neither pass makes
	// progress; interleaving the two lets a deferred decl be resolved by a
	// symbol produced by method emission (and vice versa).
	var remaining []Tokens // decls needing full parse + generate
	pending := decls
	for {
		var retry []Tokens
		var firstErr error
		for _, decl := range pending {
			p.symTracker = nil
			handled, parseErr := p.ParseDecl(decl)
			if parseErr != nil {
				var eu ErrUndefined
				if errors.As(parseErr, &eu) {
					p.rollbackSymTracker()
					retry = append(retry, decl)
					if firstErr == nil {
						firstErr = parseErr
					}
					continue
				}
				// Propagate I/O and filesystem errors (e.g. missing packages).
				// Skip everything else (parser limitations, unimplemented syntax).
				var pathErr *fs.PathError
				if errors.As(parseErr, &pathErr) {
					return out, parseErr
				}
				p.rollbackSymTracker()
				if firstErr == nil {
					firstErr = parseErr
				}
				continue
			}
			if !handled {
				remaining = append(remaining, decl)
			}
		}
		declProgress := len(retry) < len(pending)
		pending = retry

		methodProgress, mErr := p.instantiatePendingMethods()
		if mErr != nil {
			return out, mErr
		}

		if len(pending) == 0 && !methodProgress {
			break
		}
		if !declProgress && !methodProgress {
			return out, firstErr
		}
	}

	// Tag this package's own deferred decls with its import path, then prepend
	// the (already-tagged) code-gen declarations from imported source packages.
	pkgTag := ""
	if src == "" {
		pkgTag = name
	}
	merged := p.importRemaining
	p.importRemaining = nil
	for _, d := range remaining {
		merged = append(merged, DeferredDecl{PkgPath: pkgTag, Toks: d})
	}

	// Phase 2: split var blocks, sort var declarations by dependency,
	// then generate code in two passes. All symbols (including methods)
	// are registered in Phase 1 with their signatures.
	//
	// Pass 1 compiles var initializers so that all var types are resolved.
	// Pass 2 compiles func bodies and expression statements; by then every
	// global var has a concrete type, eliminating forward-reference retries.
	return p.splitAndSortVarDecls(merged), err
}

func (p *Parser) preRegisterTypes(decls []Tokens) {
	for _, decl := range decls {
		if len(decl) < 2 || decl[0].Tok != lang.Type {
			continue
		}
		if decl[1].Tok == lang.ParenBlock {
			// Grouped: type ( A struct{...}; B struct{...} )
			inner, err := p.scanBlock(decl[1].Token, false)
			if err != nil {
				continue
			}
			for _, lt := range inner.Split(lang.Semicolon) {
				if len(lt) >= 2 && lt[0].Tok == lang.Ident {
					n := lt[0].Str
					switch lt[1].Tok {
					case lang.Struct:
						p.registerStructPlaceholder(p.pkgKey(n), n)
					case lang.Interface:
						p.registerInterfacePlaceholder(p.pkgKey(n), n)
					}
				}
			}
			continue
		}
		// Single: type A struct{...} or type A interface{...}
		if len(decl) >= 3 && decl[1].Tok == lang.Ident {
			n := decl[1].Str
			switch decl[2].Tok {
			case lang.Struct:
				p.registerStructPlaceholder(p.pkgKey(n), n)
			case lang.Interface:
				p.registerInterfacePlaceholder(p.pkgKey(n), n)
			}
		}
	}
}

func (p *Parser) registerStructPlaceholder(key, short string) *vm.Type {
	// Only reuse an existing binding if it is genuinely a struct placeholder
	// awaiting SetFields. Bare type names are not package-qualified while a
	// package's source is parsed, so `key` may currently hold an unrelated type
	// from another import (e.g. a `type X uint16` whose Rtype is the shared,
	// read-only reflect.TypeOf(uint16(0))). Patching that in SetFields would
	// memcpy onto read-only memory and crash; shadow it with a fresh placeholder
	// instead. (Same guard also avoids re-patching an already-finalized struct,
	// which would corrupt the other package's type.)
	if s, ok := p.Symbols[key]; ok && s.Kind == symbol.Type &&
		s.Type != nil && s.Type.Rtype != nil &&
		s.Type.Rtype.Kind() == reflect.Struct && s.Type.Placeholder {
		return s.Type
	}
	ph := vm.NewStructType()
	ph.Name = short
	p.SymAdd(symbol.UnsetAddr, key, vm.NewValue(ph.Rtype), symbol.Type, ph)
	return ph
}

func (p *Parser) registerInterfacePlaceholder(key, short string) *vm.Type {
	// Only reuse an existing binding if it is genuinely an interface placeholder
	// awaiting finalization (parseTypeLine fills in IfaceMethods/TypeElems and
	// clears Placeholder). Otherwise the bare name may currently hold either an
	// unrelated kind from a sibling package (e.g. internal/language's `type
	// ValueError struct{...}` while parsing language's `type ValueError
	// interface{...}`) or that sibling's already-finalized interface; reusing
	// either would flip its kind / overwrite its method set and corrupt the
	// other package's qualified alias. Shadow the bare key with a fresh
	// placeholder instead.
	if s, ok := p.Symbols[key]; ok && s.Kind == symbol.Type &&
		s.Type != nil && s.Type.Rtype != nil &&
		s.Type.Rtype.Kind() == reflect.Interface && s.Type.Placeholder {
		return s.Type
	}
	ph := &vm.Type{Rtype: vm.AnyRtype, Name: short, Placeholder: true}
	p.SymAdd(symbol.UnsetAddr, key, vm.NewValue(ph.Rtype), symbol.Type, ph)
	return ph
}

// IsExported reports whether the given name starts with an upper-case letter.
func IsExported(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
}
