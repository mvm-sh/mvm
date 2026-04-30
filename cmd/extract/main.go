// Command extract parses Go package source using mvm's goparser and prints
// exported const, var, type, and func declarations to stdout.
//
// Usage:
//
//	go run ./cmd/extract <directory>
//	go run ./cmd/extract -stdlib <import-path> <directory>
//
// In -stdlib mode the binding file is written to ./core/<file>.go or
// ./ext/<file>.go relative to the current working directory.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/constant"
	"go/format"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

var (
	stdlibMode = flag.Bool("stdlib", false, "if set, generate a stdlib binding file at ./{core,ext}/<import-path>.go")
	outFile    = flag.String("o", "", "output file for raw symbol listing (ignored when -stdlib is set)")
	targetOS   = flag.String("goos", "", "target GOOS for build constraint filtering")
	targetArch = flag.String("goarch", "", "target GOARCH for build constraint filtering")
)

func main() {
	log.SetFlags(log.Lshortfile)
	flag.Parse()

	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func mainErr() error {
	args := flag.Args()
	if len(args) < 1 {
		return errors.New("usage: extract [-stdlib <import-path>] <directory>")
	}

	if *stdlibMode {
		if len(args) < 2 {
			return errors.New("usage: extract -stdlib <import-path> <directory>")
		}
		importPath, dir := args[0], args[1]
		path := filepath.Join(subDir(importPath), bindingFilename(importPath, *targetOS, *targetArch))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		return runGen(f, dir, importPath)
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
	return run(out, args[0])
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
func extract(dir string) (map[symbol.Kind][]string, map[string]string, error) {
	imports, err := extractImports(dir, *targetOS, *targetArch)
	if err != nil {
		return nil, nil, fmt.Errorf("scanning imports: %w", err)
	}
	log.Println("extract", dir, *targetOS, *targetArch)

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

	p.SetPkgfs(filepath.Dir(dir))
	if _, err := p.ParseAll(filepath.Base(dir), ""); err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}

	groups := map[symbol.Kind][]string{
		symbol.Const: {},
		symbol.Var:   {},
		symbol.Type:  {},
		symbol.Func:  {},
	}
	typedConsts := map[string]string{}

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
		}
	}

	return groups, typedConsts, nil
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
	groups, _, err := extract(dir)
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

func runGen(out io.Writer, dir, importPath string) error {
	groups, typedConsts, err := extract(dir)
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

	// Determine the Go package name (last non-version path element).
	alias := goparser.PackageName(importPath)

	var buf bytes.Buffer
	w := func(f string, args ...any) { _, _ = fmt.Fprintf(&buf, f, args...) }
	if tag := BuildTags[importPath]; tag != "" {
		w("//go:build %s\n\n", tag)
	} else if *targetOS != "" && *targetArch != "" {
		w("//go:build %s && %s\n\n", *targetOS, *targetArch)
	}
	w("// Code generated by cmd/extract; DO NOT EDIT.\n")
	w("\npackage %s\n", subDir(importPath))

	// Emit a stub file when the package exposes no extractable concrete
	// symbols (e.g. only generic functions). This keeps the generated file
	// valid Go even if there is nothing to register.
	if len(values) > 0 || len(vars) > 0 || len(types) > 0 {
		w("\nimport (\n")
		if importPath != "reflect" {
			w("\t%q\n", importPath)
		}
		w("\t\"reflect\"\n")
		w("\n\t\"github.com/mvm-sh/mvm/stdlib\"\n")
		w(")\n")
		w("\nfunc init() {\n")
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

		w("\t}\n}\n")
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		_, _ = out.Write(buf.Bytes())
		return fmt.Errorf("format generated source for %s: %w", importPath, err)
	}
	_, err = out.Write(formatted)
	return err
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

	for _, line := range strings.Split(src, "\n") {
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

// extractQuoted returns the first double-quoted string found in line.
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
