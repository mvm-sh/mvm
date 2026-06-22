// Command extract parses Go package source using mvm's goparser and emits
// bindings for a package's exported const, var, type, and func declarations.
//
// In the default mode it generates a reflect.Value binding map for an import
// path, ready to pass to (*interp.Interp).ImportPackageValues so interpreted
// code can `import "<import-path>"` and call into the native package:
//
//	go run ./cmd/extract -o bindings.go github.com/you/yourpkg
//
// With -list it prints a raw "<kind> <name>" listing of a directory; with
// -stdlib it writes an mvm stdlib binding file under ./core or ./ext.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"go/constant"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

const usageText = `extract emits Go bindings from a package's exported symbols.

Usage:
  extract [-o out.go] [-pkg name] <import-path> [dir]
  extract -list <directory>
  extract -stdlib <import-path> <directory>

The default mode generates a reflect.Value binding map for <import-path>, ready
to pass to (*interp.Interp).ImportPackageValues. The package source is located
with 'go list' unless [dir] is given. With -list it prints a raw "<kind> <name>"
listing of <directory>. With -stdlib it writes an mvm stdlib binding file under
./core or ./ext.

Flags:
`

var (
	stdlibMode = flag.Bool("stdlib", false, "generate an mvm stdlib binding file at ./{core,ext}/<import-path>.go")
	listMode   = flag.Bool("list", false, "print a raw \"<kind> <name>\" symbol listing of a directory")
	outFile    = flag.String("o", "", "output file (default stdout); ignored when -stdlib is set")
	pkgName    = flag.String("pkg", "main", "package clause for the generated bindings file")
	targetOS   = flag.String("goos", "", "target GOOS for build constraint filtering")
	targetArch = flag.String("goarch", "", "target GOARCH for build constraint filtering")
)

func main() {
	flag.Usage = func() {
		_, _ = fmt.Fprint(os.Stderr, usageText)
		flag.PrintDefaults()
	}
	flag.Parse()

	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func mainErr() error {
	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		return errors.New("missing argument")
	}

	fmt.Fprintln(os.Stderr, "extract", args[0], *targetOS, *targetArch)

	if *stdlibMode {
		if len(args) < 2 {
			flag.Usage()
			return errors.New("-stdlib needs <import-path> <directory>")
		}
		importPath, dir := args[0], args[1]
		outDir := subDir(importPath)
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return err
		}
		return runGen(outDir, dir, importPath)
	}

	out := os.Stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		out = f
	}

	if *listMode {
		return run(out, args[0])
	}

	dirOverride := ""
	if len(args) >= 2 {
		dirOverride = args[1]
	}
	return runValues(out, args[0], dirOverride)
}

// bindingFilename derives a Go filename from an import path. Slashes become
// underscores; an optional GOOS/GOARCH suffix is appended (used for syscall).
func bindingFilename(importPath, goos, goarch string) string {
	name := strings.ReplaceAll(importPath, "/", "_")
	if goos != "" && goarch != "" {
		name += "_" + goos + "_" + goarch
	}
	return name + ".go"
}

// extract parses a Go package directory and returns exported symbols grouped
// by kind, plus a map from const name to an explicit Go type wrapper needed
// to avoid `reflect.ValueOf(untyped int constant) overflows int` errors when
// the constant value cannot fit in the default Go type (e.g. uint64 > MaxInt64).
func extract(dir string) (map[symbol.Kind][]string, map[string]string, map[string]string, error) {
	imports, err := extractImports(dir, *targetOS, *targetArch)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("scanning imports of %s: %w", dir, err)
	}

	p := goparser.NewParser(golang.GoSpec, false)
	if *targetOS != "" && *targetArch != "" {
		p.SetBuildContext(*targetOS, *targetArch)
	}

	for _, imp := range imports {
		p.Packages[imp] = &symbol.Package{
			Path:   imp,
			Bin:    true,
			Values: map[string]vm.Value{},
		}
	}

	// Resolve to an absolute path before splitting into (parent dir, package base name).
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolving %s: %w", dir, err)
	}
	p.SetPkgfs(filepath.Dir(abs))
	if _, err := p.ParseAll(filepath.Base(abs), ""); err != nil {
		fmt.Fprintf(os.Stderr, "parsing %s warning: %v\n", dir, err)
	}

	groups := map[symbol.Kind][]string{
		symbol.Const: {},
		symbol.Var:   {},
		symbol.Type:  {},
		symbol.Func:  {},
	}
	typedConsts := map[string]string{}

	// constExacts records the exact arbitrary-precision form of a constant so the
	// compiler can rebuild it as an untyped Cval.
	constExacts := map[string]string{}

	for name, sym := range p.Symbols {
		if strings.ContainsAny(name, "/.#") {
			// Skip nested-scope (/.) and generic instantiations (#)
			// which cannot be expressed in a reflect binding.
			continue
		}
		if !goparser.IsExported(name) {
			continue
		}
		if _, ok := groups[sym.Kind]; !ok {
			continue
		}
		groups[sym.Kind] = append(groups[sym.Kind], name)
		if sym.Kind == symbol.Const {
			if w := constWrapFor(sym); w != "" {
				typedConsts[name] = w
			}
			if sym.Cval != nil {
				switch k := sym.Cval.Kind(); {
				case k == constant.Float:
					constExacts[name] = sym.Cval.ExactString()
				case k == constant.Int && sym.Type == nil:
					constExacts[name] = sym.Cval.ExactString()
				}
			}
		}
	}

	return groups, typedConsts, constExacts, nil
}

// constWrapFor returns the Go type name to wrap a const value with when
// reflect.ValueOf on the raw identifier would fail. The sole case is an
// untyped integer constant whose value overflows int (e.g. hash/crc64.ECMA
// = 0xC96C5795D7870F42 > math.MaxInt64). Wrapping named types is deliberately
// avoided so that bindings preserve e.g. time.Duration rather than int64.
func constWrapFor(sym *symbol.Symbol) string {
	if sym.Cval == nil || sym.Cval.Kind() != constant.Int {
		return ""
	}
	if _, exact := constant.Int64Val(sym.Cval); exact {
		return ""
	}
	if _, ok := constant.Uint64Val(sym.Cval); !ok {
		return ""
	}
	return "uint64"
}

func run(out io.Writer, dir string) error {
	groups, _, _, err := extract(dir)
	if err != nil {
		return err
	}

	for _, kind := range []symbol.Kind{symbol.Const, symbol.Var, symbol.Type, symbol.Func} {
		names := groups[kind]
		sort.Strings(names)
		for _, name := range names {
			_, _ = fmt.Fprintf(out, "%s %s\n", strings.ToLower(kind.String()), name)
		}
	}
	return nil
}

// runValues generates a reflect.Value binding map for importPath and writes it
// to out. The package source is located with `go list` unless dirOverride is
// given; dirOverride lets callers bind a package `go list` cannot resolve.
func runValues(out io.Writer, importPath, dirOverride string) error {
	canonPath, dir, alias, err := resolvePkg(importPath, dirOverride)
	if err != nil {
		return err
	}
	groups, typedConsts, _, err := extract(dir)
	if err != nil {
		return err
	}
	src, err := buildValuesFile(canonPath, alias, *pkgName, groups, typedConsts)
	if err != nil {
		return err
	}
	_, err = out.Write(src)
	return err
}

// resolvePkg returns the canonical import path, source directory, and declared
// package name for importPath. When dirOverride is empty it consults `go list`,
// which canonicalizes a relative or "." argument (e.g. run inside the package
// dir) into the real import path used by the generated `import` and map key.
// Otherwise importPath is taken as canonical (the caller supplied it) and the
// name is read from the override directory, for packages `go list` cannot
// resolve. The declared name -- not the path's last element -- is what
// `import "path"` binds, so it is the right selector alias even for versioned
// paths like gopkg.in/yaml.v3 (package "yaml").
func resolvePkg(importPath, dirOverride string) (canonPath, dir, alias string, err error) {
	if dirOverride == "" {
		b, lerr := exec.Command("go", "list", "-f", "{{.ImportPath}}\t{{.Dir}}\t{{.Name}}", importPath).Output()
		if lerr != nil {
			return "", "", "", fmt.Errorf("go list %s: %w", importPath, lerr)
		}
		listed := strings.TrimSpace(string(b))
		if strings.ContainsRune(listed, '\n') {
			return "", "", "", fmt.Errorf("go list %s: matched multiple packages; pass a single import path", importPath)
		}
		parts := strings.Split(listed, "\t")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", fmt.Errorf("go list %s: unexpected output %q", importPath, b)
		}
		return parts[0], parts[1], parts[2], nil
	}
	alias = packageNameFromDir(dirOverride)
	if alias == "" {
		alias = goparser.PackageName(importPath)
	}
	return importPath, dirOverride, alias, nil
}

// packageNameFromDir returns the declared package name of the buildable, non-
// test package in dir, or "" if there is none. It uses go/build so build
// constraints are honored (e.g. a `//go:build ignore` generator file declaring
// `package main` is skipped) and the name comes from a real parse (so a
// trailing comment on the package clause is not included).
func packageNameFromDir(dir string) string {
	bp, err := build.ImportDir(dir, 0)
	if err != nil {
		return ""
	}
	return bp.Name
}

// buildValuesFile renders a gofmt'd Go source file declaring
//
//	var <alias>Values = map[string]map[string]reflect.Value{ "<importPath>": {...} }
//
// with one reflect.ValueOf entry per exported symbol: consts and funcs by
// value, vars by address, types as a typed nil pointer. The shape matches
// stdlib.Values, so the map can be passed straight to ImportPackageValues.
func buildValuesFile(importPath, alias, pkgName string, groups map[symbol.Kind][]string, typedConsts map[string]string) ([]byte, error) {
	values := make([]string, 0, len(groups[symbol.Const])+len(groups[symbol.Func]))
	values = append(values, groups[symbol.Const]...)
	values = append(values, groups[symbol.Func]...)
	sort.Strings(values)
	vars := append([]string(nil), groups[symbol.Var]...)
	sort.Strings(vars)
	types := append([]string(nil), groups[symbol.Type]...)
	sort.Strings(types)
	hasSymbols := len(values)+len(vars)+len(types) > 0

	var buf bytes.Buffer
	w := func(f string, args ...any) { _, _ = fmt.Fprintf(&buf, f, args...) }
	w("// Code generated by cmd/extract; DO NOT EDIT.\n\n")
	w("package %s\n\n", pkgName)
	w("import (\n")
	// Skip the package's own import when it is reflect itself, which is always
	// imported below; otherwise it would be a duplicate import.
	if hasSymbols && importPath != "reflect" {
		w("\t%q\n\n", importPath)
	}
	w("\t\"reflect\"\n")
	w(")\n\n")
	w("var %sValues = map[string]map[string]reflect.Value{\n", alias)
	w("\t%q: {\n", importPath)
	for _, name := range values {
		if wrap, ok := typedConsts[name]; ok {
			w("\t\t%q: reflect.ValueOf(%s(%s.%s)),\n", name, wrap, alias, name)
		} else {
			w("\t\t%q: reflect.ValueOf(%s.%s),\n", name, alias, name)
		}
	}
	for _, name := range vars {
		w("\t\t%q: reflect.ValueOf(&%s.%s),\n", name, alias, name)
	}
	for _, name := range types {
		w("\t\t%q: reflect.ValueOf((*%s.%s)(nil)),\n", name, alias, name)
	}
	w("\t},\n")
	w("}\n")

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return buf.Bytes(), fmt.Errorf("format generated source for %s: %w", importPath, err)
	}
	return formatted, nil
}

func runGen(outDir, dir, importPath string) error {
	groups, typedConsts, constExacts, err := extract(dir)
	if err != nil {
		return err
	}

	// Merge const, var, func into values; types are handled separately.
	values := make([]string, 0, len(groups[symbol.Const])+len(groups[symbol.Func]))
	values = append(values, groups[symbol.Const]...)
	values = append(values, groups[symbol.Func]...)
	sort.Strings(values)

	vars := groups[symbol.Var]
	sort.Strings(vars)
	types := groups[symbol.Type]
	sort.Strings(types)

	// Split out symbols that are gated by a per-symbol build tag. The base
	// file gets the remainder; each tag gets a supplement file.
	tagged := taggedSymbols(importPath)
	baseValues, taggedValues := splitByTag(values, tagged)
	baseVars, taggedVars := splitByTag(vars, tagged)
	baseTypes, taggedTypes := splitByTag(types, tagged)

	baseName := bindingFilename(importPath, *targetOS, *targetArch)
	baseTag := BuildTags[importPath]
	if baseTag == "" && *targetOS != "" && *targetArch != "" {
		baseTag = fmt.Sprintf("%s && %s", *targetOS, *targetArch)
	}
	if err := writeBinding(filepath.Join(outDir, baseName), importPath, baseTag, false, baseValues, baseVars, baseTypes, typedConsts, constExacts); err != nil {
		return err
	}

	for _, tag := range sortedKeys(tagged) {
		tv, tvars, ttypes := taggedValues[tag], taggedVars[tag], taggedTypes[tag]
		if len(tv) == 0 && len(tvars) == 0 && len(ttypes) == 0 {
			continue
		}
		suppName := supplementFilename(importPath, tag, *targetOS, *targetArch)
		// Float constants are platform-independent and never tagged, so the
		// high-precision registry is emitted only in the base file.
		if err := writeBinding(filepath.Join(outDir, suppName), importPath, tag, true, tv, tvars, ttypes, typedConsts, nil); err != nil {
			return err
		}
	}
	return nil
}

// writeBinding emits a single binding file. When supplement is true, the file
// mutates the existing stdlib.Values entry rather than overwriting it; this
// allows multiple files (one base + per-tag supplements) to register symbols
// for the same import path.
func writeBinding(path, importPath, buildTag string, supplement bool, values, vars, types []string, typedConsts, constExacts map[string]string) error {
	alias := goparser.PackageName(importPath)

	var buf bytes.Buffer
	w := func(f string, args ...any) { _, _ = fmt.Fprintf(&buf, f, args...) }
	if buildTag != "" {
		w("//go:build %s\n\n", buildTag)
	}
	w("// Code generated by cmd/extract; DO NOT EDIT.\n")
	w("\npackage %s\n", subDir(importPath))

	hasSymbols := len(values) > 0 || len(vars) > 0 || len(types) > 0

	// For the base file, emit a stub when there are no symbols so an empty
	// package still produces valid Go. Supplement files with no symbols are
	// not produced by callers, so we skip emitting one here.
	if hasSymbols {
		w("\nimport (\n")
		if importPath != "reflect" {
			w("\t%q\n", importPath)
		}
		w("\t\"reflect\"\n")
		w("\n\t\"github.com/mvm-sh/mvm/stdlib\"\n")
		w(")\n")
		w("\nfunc init() {\n")
		if supplement {
			w("\tm := stdlib.Values[%q]\n", importPath)
			for _, name := range values {
				if wrap, ok := typedConsts[name]; ok {
					w("\tm[%q] = reflect.ValueOf(%s(%s.%s))\n", name, wrap, alias, name)
				} else {
					w("\tm[%q] = reflect.ValueOf(%s.%s)\n", name, alias, name)
				}
			}
			for _, name := range vars {
				w("\tm[%q] = reflect.ValueOf(&%s.%s)\n", name, alias, name)
			}
			for _, name := range types {
				w("\tm[%q] = reflect.ValueOf((*%s.%s)(nil))\n", name, alias, name)
			}
		} else {
			w("\tstdlib.Values[%q] = map[string]reflect.Value{\n", importPath)
			for _, name := range values {
				if wrap, ok := typedConsts[name]; ok {
					w("\t\t%q: reflect.ValueOf(%s(%s.%s)),\n", name, wrap, alias, name)
				} else {
					w("\t\t%q: reflect.ValueOf(%s.%s),\n", name, alias, name)
				}
			}
			for _, name := range vars {
				w("\t\t%q: reflect.ValueOf(&%s.%s),\n", name, alias, name)
			}
			for _, name := range types {
				w("\t\t%q: reflect.ValueOf((*%s.%s)(nil)),\n", name, alias, name)
			}
			w("\t}\n")
		}
		if len(constExacts) > 0 {
			names := make([]string, 0, len(constExacts))
			for name := range constExacts {
				names = append(names, name)
			}
			sort.Strings(names)
			w("\tstdlib.ConstValues[%q] = map[string]string{\n", importPath)
			for _, name := range names {
				w("\t\t%q: %q,\n", name, constExacts[name])
			}
			w("\t}\n")
		}
		w("}\n")
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	formatted, ferr := format.Source(buf.Bytes())
	if ferr != nil {
		_, _ = out.Write(buf.Bytes())
		return fmt.Errorf("format generated source for %s: %w", importPath, ferr)
	}
	_, err = out.Write(formatted)
	return err
}

func taggedSymbols(importPath string) map[string]string {
	tags := SymbolBuildTags[importPath]
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]string)
	for tag, names := range tags {
		for _, n := range names {
			out[n] = tag
		}
	}
	return out
}

func splitByTag(names []string, tagged map[string]string) (base []string, byTag map[string][]string) {
	byTag = map[string][]string{}
	for _, n := range names {
		if tag, ok := tagged[n]; ok {
			byTag[tag] = append(byTag[tag], n)
		} else {
			base = append(base, n)
		}
	}
	return base, byTag
}

func sortedKeys(tagged map[string]string) []string {
	seen := map[string]bool{}
	for _, tag := range tagged {
		seen[tag] = true
	}
	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func supplementFilename(importPath, tag, goos, goarch string) string {
	suffix, ok := tagFileSuffix[tag]
	if !ok {
		// Fallback: replace dots in the tag (e.g. "go1.26" -> "go1_26").
		suffix = strings.ReplaceAll(tag, ".", "_")
	}
	name := strings.ReplaceAll(importPath, "/", "_") + "_" + suffix
	if goos != "" && goarch != "" {
		name += "_" + goos + "_" + goarch
	}
	return name + ".go"
}

// extractImports reads all .go files in dir (excluding _test.go) and returns
// the deduplicated list of import paths found. When goos and goarch are non-empty,
// file name filtering uses the specified platform instead of the host platform.
func extractImports(dir, goos, goarch string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if goos != "" && goarch != "" {
			if !goparser.MatchFileNameFor(e.Name(), goos, goarch) {
				continue
			}
		} else if !goparser.MatchFileName(e.Name(), nil) {
			continue
		}
		buf, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, imp := range scanImports(string(buf)) {
			seen[imp] = true
		}
	}

	imports := make([]string, 0, len(seen))
	for imp := range seen {
		imports = append(imports, imp)
	}
	sort.Strings(imports)
	return imports, nil
}

// scanImports extracts import paths from Go source text using simple line scanning.
func scanImports(src string) []string {
	var imports []string
	inBlock := false

	for line := range strings.SplitSeq(src, "\n") {
		line = strings.TrimSpace(line)

		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			if p := extractQuoted(line); p != "" {
				imports = append(imports, p)
			}
			continue
		}

		if strings.HasPrefix(line, "import (") {
			inBlock = true
			continue
		}
		if strings.HasPrefix(line, "import ") {
			if p := extractQuoted(line); p != "" {
				imports = append(imports, p)
			}
		}
	}
	return imports
}

func extractQuoted(line string) string {
	i := strings.IndexByte(line, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(line[i+1:], '"')
	if j < 0 {
		return ""
	}
	return line[i+1 : i+1+j]
}
