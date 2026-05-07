package goparser

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	return out, nil
}

func (p *Parser) importSrc(pkgPath string) (err error) {
	// Save and restore parser state so the imported package's
	// "package" declaration does not conflict with the current one,
	// and so includeTests stays local to the top-level test target
	// rather than leaking into transitive imports.
	savedPkgName := p.pkgName
	savedIncludeTests := p.includeTests
	p.pkgName = ""
	p.includeTests = false
	defer func() {
		p.pkgName = savedPkgName
		p.includeTests = savedIncludeTests
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
	// for code generation by the outer ParseAll / Compile.
	p.importRemaining = append(p.importRemaining, remaining...)

	// Collect exported symbols into a Package entry and create
	// qualified aliases (e.g. "example.com/pkg1.V") so the compiler
	// can resolve pkg.Member accesses.
	pkg := &symbol.Package{
		Path:   pkgPath,
		Values: map[string]vm.Value{},
	}
	var genericKeys []string
	for k, s := range p.Symbols {
		if existing[k] == s || !IsExported(k) {
			continue
		}
		if s.Kind == symbol.Generic {
			genericKeys = append(genericKeys, k)
			continue
		}
		pkg.Values[k] = s.Value
	}
	// Create qualified aliases after the loop to avoid mutating p.Symbols during iteration.
	for k := range pkg.Values {
		p.Symbols[pkgPath+"."+k] = p.Symbols[k]
	}
	for _, k := range genericKeys {
		p.Symbols[pkgPath+"."+k] = p.Symbols[k]
	}
	p.Packages[pkgPath] = pkg

	return nil
}

// ParseAll parses code and its dependencies, and returns slices of Tokens or an error.
func (p *Parser) ParseAll(name, src string) (out []Tokens, err error) {
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
	// mutual, and self-references can resolve during parsing.
	// Placeholders are untracked: they survive the retry loop cleanup.
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

	// Include code-gen declarations from imported source packages.
	if len(p.importRemaining) > 0 {
		remaining = append(p.importRemaining, remaining...)
		p.importRemaining = nil
	}

	// Phase 2: split var blocks, sort var declarations by dependency,
	// then generate code in two passes. All symbols (including methods)
	// are registered in Phase 1 with their signatures.
	//
	// Pass 1 compiles var initializers so that all var types are resolved.
	// Pass 2 compiles func bodies and expression statements; by then every
	// global var has a concrete type, eliminating forward-reference retries.
	remaining = p.splitAndSortVarDecls(remaining)
	return remaining, err
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
						p.registerStructPlaceholder(n, n)
					case lang.Interface:
						p.registerInterfacePlaceholder(n, n)
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
				p.registerStructPlaceholder(n, n)
			case lang.Interface:
				p.registerInterfacePlaceholder(n, n)
			}
		}
	}
}

func (p *Parser) registerStructPlaceholder(key, short string) *vm.Type {
	if s, ok := p.Symbols[key]; ok && s.Kind == symbol.Type {
		return s.Type
	}
	ph := vm.NewStructType()
	ph.Name = short
	p.SymAdd(symbol.UnsetAddr, key, vm.NewValue(ph.Rtype), symbol.Type, ph)
	return ph
}

func (p *Parser) registerInterfacePlaceholder(key, short string) *vm.Type {
	if s, ok := p.Symbols[key]; ok && s.Kind == symbol.Type {
		return s.Type
	}
	ph := &vm.Type{Rtype: vm.AnyRtype, Name: short}
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
