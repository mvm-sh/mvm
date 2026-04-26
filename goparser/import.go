package goparser

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

func (p *Parser) importSrc(pkgPath string) (err error) {
	// Save and restore parser state so the imported package's
	// "package" declaration does not conflict with the current one.
	savedPkgName := p.pkgName
	p.pkgName = ""
	defer func() { p.pkgName = savedPkgName }()

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
		return err
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
		// Get content from file(s). Primary pkgfs first; stdlib fallback resolves
		// embedded generics-first packages (cmp, slices, ...) when the user pkgfs
		// does not provide them.
		if p.pkgfs == nil {
			p.pkgfs = os.DirFS(".")
		}
		fsys := p.pkgfs
		fi, err := fs.Stat(fsys, name)
		if err != nil && p.stdlibfs != nil {
			if fi2, err2 := fs.Stat(p.stdlibfs, name); err2 == nil {
				fsys = p.stdlibfs
				fi = fi2
				err = nil
			}
		}
		if err != nil {
			return out, err
		}
		if fi.IsDir() {
			files, err := fs.ReadDir(fsys, name)
			if err != nil {
				return out, err
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
					continue
				}
				if !MatchFileName(f.Name(), p.buildCtx) {
					continue
				}
				buf, err := fs.ReadFile(fsys, name+"/"+f.Name())
				if err != nil {
					return out, err
				}
				src := string(buf)
				if !matchBuildDirective(src, p.buildCtx) {
					continue
				}
				d, err := p.scanDecls(src)
				p.PosBase = p.Sources.Add(name, src)
				if err != nil {
					return out, err
				}
				decls = append(decls, d...)
			}
		}
	} else {
		decls, err = p.scanDecls(src)
		srcName := name
		if len(name) >= 2 && name[1] == ':' && (name[0] == 'f' || name[0] == 'm') {
			srcName = name[2:]
		}
		p.PosBase = p.Sources.Add(srcName, src)
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
