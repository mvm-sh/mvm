package goparser

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
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
		// Fallback for `mvm test <stdlib-path>` on bridge-only packages
		// (strings, bytes, fmt, ...): no source in pkgfs/stdlibfs/remotefs,
		// but the bridge is registered. Pull external `package X_test`
		// files from testSrcFS ($GOROOT/src) so their Test* funcs can run
		// against the existing reflect bindings.
		if includeTests && p.testSrcFS != nil {
			if pkg, ok := p.Packages[importPath]; ok && pkg.Bin {
				return p.loadBridgedTestSources(importPath)
			}
		}
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a package directory", importPath)
	}
	return p.collectPackageSources(fsys, importPath, importPath, includeTests)
}

// LoadLocalPackageSources is LoadPackageSources for `mvm test <dir>`: a package
// rooted at a local OS directory rather than an import path in the FS chain.
func (p *Parser) LoadLocalPackageSources(absDir string) ([]PackageSource, error) {
	return p.collectPackageSources(os.DirFS(absDir), ".", "", true)
}

func (p *Parser) collectPackageSources(fsys fs.FS, dir, importPath string, includeTests bool) ([]PackageSource, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []PackageSource
	sawTestFile := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			sawTestFile = true
		}
		if !includeTests && strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if p.testSkipFiles[e.Name()] {
			continue
		}
		if !MatchFileName(e.Name(), p.buildCtx) {
			continue
		}
		buf, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		src := string(buf)
		if !matchBuildDirective(src, p.buildCtx) {
			continue
		}
		p.scanEmbeds(fsys, dir, src)
		out = append(out, PackageSource{Name: e.Name(), Content: src})
	}
	// Append overlays before the test handling, so the external-only unit
	// (built from _test.go files) never picks up a package-name mismatch.
	out = append(out, sourceOverlay(importPath)...)
	// External test files must compile as a standalone unit.
	if includeTests {
		names := make([]string, len(out))
		var mainPkg string
		for i, s := range out {
			names[i] = extractPackageName(s.Content)
			if mainPkg == "" && names[i] != "" && !strings.HasSuffix(s.Name, "_test.go") {
				mainPkg = names[i]
			}
		}
		// Only one unit can be returned, so prefer internal (package X) tests
		// in-unit; serve external (package X_test) tests standalone otherwise.
		hasInternal := false
		for i := range out {
			if strings.HasSuffix(out[i].Name, "_test.go") && names[i] == mainPkg {
				hasInternal = true
				break
			}
		}
		var external []PackageSource
		if mainPkg != "" {
			external = p.externalTestFiles(out, names, mainPkg, dir)
		}
		// A local-dir external-only target (importPath "") is a single compile, so
		// keep its tests inline as the unit. An import-path target's tests are
		// stashed by the two-unit path below, whose retry loop the split speeds up.
		if !hasInternal && len(external) > 0 && importPath == "" {
			return external, nil
		}
		if importPath != "" && p.testSrcFS != nil && !sawTestFile {
			if ext, terr := p.loadBridgedTestSources(importPath); terr == nil && len(ext) > 0 {
				return ext, nil
			}
		}
		if mainPkg != "" {
			// Return the internal-augmented unit; stash the external files for
			// `mvm test` to load as a second unit (see ExternalTestSources).
			// Qualify names with Go's external-test pseudo package path
			// (<importPath>_test/) so runtime.Caller frames report
			// "<importPath>_test.TestX" like the gc toolchain.
			if importPath != "" {
				for i := range external {
					external[i].Name = importPath + "_test/" + external[i].Name
				}
			}
			p.externalTests = external
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

func (p *Parser) externalTestFiles(out []PackageSource, names []string, mainPkg, dir string) []PackageSource {
	var external []PackageSource
	for i, s := range out {
		if !strings.HasSuffix(s.Name, "_test.go") || names[i] != mainPkg+"_test" {
			continue
		}
		if bad := p.firstUnresolvableImport(p.extractImports(s.Content)); bad != "" {
			p.noteUnresolvableSkip(dir, s.Name, bad)
			continue
		}
		external = append(external, s)
	}
	return external
}

// ExternalTestSources returns the external `package X_test` files set aside by the
// last test-mode load, for `mvm test` to load as a second unit.
func (p *Parser) ExternalTestSources() []PackageSource { return p.externalTests }

func (p *Parser) loadBridgedTestSources(importPath string) ([]PackageSource, error) {
	fi, err := fs.Stat(p.testSrcFS, importPath)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: not a package directory", importPath)
	}
	entries, err := fs.ReadDir(p.testSrcFS, importPath)
	if err != nil {
		return nil, err
	}
	wantPkg := path.Base(importPath) + "_test"
	var out []PackageSource
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if p.testSkipFiles[e.Name()] {
			continue
		}
		if !MatchFileName(e.Name(), p.buildCtx) {
			continue
		}
		buf, err := fs.ReadFile(p.testSrcFS, importPath+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		src := string(buf)
		if !matchBuildDirective(src, p.buildCtx) {
			continue
		}
		if extractPackageName(src) != wantPkg {
			continue
		}
		if bad := p.firstUnresolvableImport(p.extractImports(src)); bad != "" {
			p.noteUnresolvableSkip(importPath, e.Name(), bad)
			continue
		}
		out = append(out, PackageSource{Name: e.Name(), Content: src})
	}
	return out, nil
}

// PublishCompiledPackage registers the just-compiled target as p.Packages[pkgPath]
// so a later unit importing it (an external `package X_test`) reuses these symbols
// instead of re-importing the source (which would clobber them). No-op if known.
func (p *Parser) PublishCompiledPackage(pkgPath string) {
	if _, ok := p.Packages[pkgPath]; ok {
		return
	}
	pkg := &symbol.Package{Path: pkgPath, Values: map[string]vm.Value{}}
	qualifiedPrefix := pkgPath + "."
	for k, s := range p.Symbols {
		if !strings.HasPrefix(k, qualifiedPrefix) {
			continue
		}
		short := k[len(qualifiedPrefix):]
		if strings.ContainsAny(short, "./*") {
			continue // method or nested key
		}
		publishValue(pkg.Values, short, s)
	}
	p.Packages[pkgPath] = pkg
}

func publishValue(values map[string]vm.Value, name string, s *symbol.Symbol) {
	if isScopedKey(name) || s.Kind == symbol.Generic || !IsExported(name) {
		return
	}
	values[name] = s.Value
}

func (p *Parser) noteUnresolvableSkip(importPath, file, badImport string) {
	if p.testSkipFiles == nil {
		p.testSkipFiles = map[string]bool{}
	}
	if p.testSkipFiles[file] {
		return
	}
	p.testSkipFiles[file] = true
	fmt.Fprintf(os.Stderr, "mvm test: skipping %s/%s: cannot resolve import %q\n", importPath, file, badImport)
}

func (p *Parser) firstUnresolvableImport(imports []string) string {
	for _, ip := range imports {
		if _, ok := p.Packages[ip]; ok {
			continue
		}
		resolved := false
		for _, fb := range []fs.FS{p.stdlibfs, p.remotefs} {
			if fb == nil {
				continue
			}
			if fi, err := fs.Stat(fb, ip); err == nil && fi.IsDir() {
				resolved = true
				break
			}
		}
		if !resolved {
			return ip
		}
	}
	return ""
}

func (p *Parser) extractImports(src string) []string {
	toks, err := p.Scan(src, false)
	if err != nil {
		return nil
	}
	var out []string
	for i := 0; i < len(toks); i++ {
		if toks[i].Tok != lang.Import {
			continue
		}
		// Grouped `import (...)`: re-scan the ParenBlock body; each String is a path.
		if i+1 < len(toks) && toks[i+1].Tok == lang.ParenBlock {
			if inner, ierr := p.Scan(toks[i+1].Block(), false); ierr == nil {
				for j := range inner {
					if inner[j].Tok == lang.String {
						out = append(out, inner[j].Block())
					}
				}
			}
			i++
			continue
		}
		// Single spec: the path is the next String before ';'.
		for j := i + 1; j < len(toks) && toks[j].Tok != lang.Semicolon; j++ {
			if toks[j].Tok == lang.String {
				out = append(out, toks[j].Block())
				break
			}
		}
	}
	return out
}

func extractPackageName(src string) string {
	// skipped; block comments before `package` are not handled (Go style places
	// `package` directly after build tags, so this is sufficient in practice).
	for src != "" {
		var line string
		if before, after, ok0 := strings.Cut(src, "\n"); ok0 {
			line, src = before, after
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

	existing := make(map[string]*symbol.Symbol, len(p.Symbols))
	maps.Copy(existing, p.Symbols)

	remaining, err := p.ParseAll(pkgPath, "")
	if err != nil {
		return fmt.Errorf("while importing %q: %w", pkgPath, err)
	}

	p.importRemaining = append(p.importRemaining, remaining...)

	pkg := &symbol.Package{
		Path:   pkgPath,
		Name:   p.pkgName, // declared name, set by parsePackageDecl during ParseAll
		Values: map[string]vm.Value{},
	}
	var newKeys []string
	qualifiedPrefix := pkgPath + "."
	for k, s := range p.Symbols {
		if existing[k] == s {
			continue
		}
		if strings.HasPrefix(k, qualifiedPrefix) {
			publishValue(pkg.Values, k[len(qualifiedPrefix):], s)
			continue
		}
		if isScopedKey(k) {
			continue
		}
		newKeys = append(newKeys, k)
		publishValue(pkg.Values, k, s)
	}
	// Create qualified aliases after the loop to avoid mutating p.Symbols during iteration.
	for _, k := range newKeys {
		s := p.Symbols[k]
		if s.Kind == symbol.Type {
			cp := *s
			s = &cp
		}
		qk := QualifyName(pkgPath, k)
		p.Symbols[qk] = s
		p.Seg.Add(qk)
	}
	p.Packages[pkgPath] = pkg

	return nil
}

// ParseAll parses code and its dependencies, and returns the still-to-be-
// code-generated declarations (func bodies, var initializers), each tagged
// with its originating package path, or an error.
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

	// Source packages tag their deferred decls with the import path; the main
	// package / REPL (src != "") uses bare keys.
	pkgTag := ""
	if src == "" {
		pkgTag = name
	}
	return p.resolveDecls(decls, pkgTag)
}

// ParseAllFiles parses a set of in-memory source files as a SINGLE compile unit
// (one package) and returns the still-to-be-code-generated declarations.
func (p *Parser) ParseAllFiles(sources []PackageSource) (out []DeferredDecl, err error) {
	var decls []Tokens
	for _, s := range sources {
		p.PosBase = p.Sources.Add(s.Name, s.Content)
		d, derr := p.scanDecls(s.Content)
		if derr != nil {
			return out, derr
		}
		decls = append(decls, d...)
	}
	return p.resolveDecls(decls, "")
}

func (p *Parser) resolveDecls(decls []Tokens, pkgTag string) (out []DeferredDecl, err error) {
	savedBatch := p.batchFuncDecls
	p.batchFuncDecls = map[string]bool{}
	defer func() { p.batchFuncDecls = savedBatch }()

	// Fresh generation per resolveDecls (nestable), gating type reuse; see reuseDeclaredType.
	savedGen := p.curGen
	p.genCounter++
	p.curGen = p.genCounter
	defer func() { p.curGen = savedGen }()

	p.preRegisterTypes(decls)
	p.preRegisterGenericFuncs(decls)

	// Phase 1: resolve all declarations and expand generic methods in a
	// single fixed-point loop.
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
				// Propagate I/O and filesystem errors (e.g. missing packages),
				// constant-overflow, and redeclaration errors (hard compile
				// errors, not parser limitations). Skip everything else (parser
				// limitations, unimplemented syntax).
				var pathErr *fs.PathError
				if errors.As(parseErr, &pathErr) {
					return out, parseErr
				}
				var overflowErr ErrConstOverflow
				if errors.As(parseErr, &overflowErr) {
					return out, parseErr
				}
				var redeclErr ErrRedeclared
				if errors.As(parseErr, &redeclErr) {
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
			// A forward reference deferred a method body (ErrUndefined): retryable,
			// like a deferred decl. Anything else is a hard compile error.
			var eu ErrUndefined
			if !errors.As(mErr, &eu) {
				return out, mErr
			}
			if firstErr == nil {
				firstErr = mErr
			}
		}

		if len(pending) == 0 && !methodProgress && mErr == nil {
			break
		}
		if !declProgress && !methodProgress {
			return out, firstErr
		}
	}

	// Tag this package's own deferred decls with pkgTag, then prepend the
	// (already-tagged) code-gen declarations from imported source packages.
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

func (p *Parser) preRegisterGenericFuncs(decls []Tokens) {
	for _, decl := range decls {
		if len(decl) < 4 || decl[0].Tok != lang.Func ||
			decl[1].Tok != lang.Ident || decl[2].Tok != lang.BracketBlock {
			continue
		}
		name := decl[1].Str
		if name == "_" || name == "init" {
			continue
		}
		key := p.pkgKey(name)
		if s, ok := p.Symbols[key]; ok && s.Kind == symbol.Generic {
			continue // already registered (real decl parsed, or a duplicate pre-pass)
		}
		params, err := p.parseTypeParamList(decl[2].Token)
		if err != nil {
			continue // let registerFunc surface the error during the loop
		}
		p.SymSet(key, p.genericFuncSymbol(name, params, decl, nil))
	}
}

func (p *Parser) recordTypeGen(t *vm.Type) {
	if p.typeGen == nil {
		p.typeGen = map[*vm.Type]int{}
	}
	p.typeGen[t] = p.curGen
}

// reuseDeclaredType returns an existing same-key type to reuse, or nil.
// It reuses an unfilled placeholder, or one filled in the current generation so a
// fixpoint re-parse of a grouped decl keeps one identity instead of minting a twin.
// A type filled by a prior compile (REPL redefinition) is older, so it gets a fresh one.
func (p *Parser) reuseDeclaredType(key string, kind reflect.Kind) *vm.Type {
	s, ok := p.Symbols[key]
	if !ok || s.Kind != symbol.Type || s.Type == nil || s.Type.Kind() != kind {
		return nil
	}
	if s.Type.Placeholder || p.typeGen[s.Type] == p.curGen {
		return s.Type
	}
	return nil
}

func (p *Parser) registerStructPlaceholder(key, short string) *vm.Type {
	if t := p.reuseDeclaredType(key, reflect.Struct); t != nil {
		return t
	}
	ph := vm.NewStructType(short)
	ph.Name = short
	p.recordTypeGen(ph)
	p.SymAdd(symbol.UnsetAddr, key, typeTokenValue(ph), symbol.Type, ph)
	return ph
}

// registerNamedPlaceholder registers or reuses a kind-agnostic placeholder for a
// self-referential composite named type, filled in place by parseTypeLine.
func (p *Parser) registerNamedPlaceholder(key, short string) *vm.Type {
	if s, ok := p.Symbols[key]; ok && s.Kind == symbol.Type && s.Type != nil &&
		(s.Type.Placeholder || p.typeGen[s.Type] == p.curGen) {
		return s.Type
	}
	ph := &vm.Type{Name: short, Placeholder: true}
	p.recordTypeGen(ph)
	p.SymAdd(symbol.UnsetAddr, key, typeTokenValue(ph), symbol.Type, ph)
	return ph
}

func (p *Parser) registerInterfacePlaceholder(key, short string) *vm.Type {
	if t := p.reuseDeclaredType(key, reflect.Interface); t != nil {
		return t
	}
	ph := &vm.Type{Rtype: vm.AnyRtype, Name: short, Placeholder: true}
	p.recordTypeGen(ph)
	p.SymAdd(symbol.UnsetAddr, key, typeTokenValue(ph), symbol.Type, ph)
	return ph
}

// IsExported reports whether the given name starts with an upper-case letter.
func IsExported(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
}
