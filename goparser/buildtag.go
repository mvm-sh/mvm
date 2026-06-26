package goparser

import (
	"go/build/constraint"
	"go/version"
	"runtime"
	"strings"
)

// buildContext holds the target platform for build constraint evaluation.
type buildContext struct {
	GOOS      string
	GOARCH    string
	GoVersion string          // major.minor only, e.g. "go1.24"
	tags      map[string]bool // extra satisfied build tags (besides GOOS/GOARCH/unix/go1.x)
}

func defaultBuildContext() *buildContext {
	return &buildContext{
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		GoVersion: version.Lang(runtime.Version()),
		tags:      defaultTags(),
	}
}

// defaultTags are the configurable build tags mvm sets by default (purego/safe
// are universal invariants handled in matchTag). nethttpomithttp2 drops
// net/http's bundled HTTP/2 stack -- mvm interprets net/http HTTP/1.1-only, so
// the HTTP/2 bundle (and its x/net/http2/hpack dep) is excluded by its own
// //go:build constraint instead of by a mirror patch.
func defaultTags() map[string]bool {
	return map[string]bool{
		"nethttpomithttp2": true,
	}
}

// MatchFileName reports whether name (a .go file basename) matches the
// given build context's GOOS/GOARCH constraints encoded in the file name.
func MatchFileName(name string, ctx *buildContext) bool {
	if ctx == nil {
		ctx = defaultBuildContext()
	}
	name = strings.TrimSuffix(name, ".go")

	_, after, ok := strings.Cut(name, "_")
	if !ok {
		return true // no underscore, no constraint
	}
	tags := strings.Split(after, "_")
	// Mirror go/build goodOSArchFile: a trailing "test" element is the _test.go
	// marker, not a constraint, so drop it before reading the GOOS/GOARCH suffix
	// (e.g. arith_s390x_test.go is s390x-only, like arith_s390x.go).
	if n := len(tags); n > 0 && tags[n-1] == "test" {
		tags = tags[:n-1]
	}

	n := len(tags)
	if n >= 2 && knownOS[tags[n-2]] && knownArch[tags[n-1]] {
		return tags[n-2] == ctx.GOOS && tags[n-1] == ctx.GOARCH
	}
	if n >= 1 && knownOS[tags[n-1]] {
		return tags[n-1] == ctx.GOOS
	}
	if n >= 1 && knownArch[tags[n-1]] {
		return tags[n-1] == ctx.GOARCH
	}
	return true
}

func matchBuildDirective(src string, ctx *buildContext) bool {
	var plusExpr constraint.Expr // AND-combined // +build lines
	for src != "" {
		var line string
		if before, after, ok := strings.Cut(src, "\n"); ok {
			line, src = before, after
		} else {
			line, src = src, ""
		}
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			break // reached non-comment line (e.g. package)
		}
		if constraint.IsGoBuild(line) {
			// //go:build takes precedence over any // +build lines.
			expr, err := constraint.Parse(line)
			if err != nil {
				return false
			}
			return expr.Eval(ctx.matchTag)
		}
		if constraint.IsPlusBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				return false
			}
			if plusExpr == nil {
				plusExpr = expr
			} else {
				plusExpr = &constraint.AndExpr{X: plusExpr, Y: expr}
			}
		}
	}
	if plusExpr != nil {
		return plusExpr.Eval(ctx.matchTag)
	}
	return true
}

func (ctx *buildContext) matchTag(tag string) bool {
	if tag == ctx.GOOS || tag == ctx.GOARCH {
		return true
	}
	if tag == "unix" {
		return unixOS[ctx.GOOS]
	}
	if strings.HasPrefix(tag, "go1.") {
		return version.Compare(ctx.GoVersion, tag) >= 0
	}
	// purego/safe: mvm interprets source (no assembly, unsafe-free reflect), so
	// the conventional pure-Go fallback tags are always satisfied.
	if tag == "purego" || tag == "safe" {
		return true
	}
	return ctx.tags[tag]
}

var knownOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
}

var knownArch = map[string]bool{
	"386": true, "amd64": true, "arm": true, "arm64": true,
	"loong64": true, "mips": true, "mips64": true, "mips64le": true,
	"mipsle": true, "ppc64": true, "ppc64le": true, "riscv64": true,
	"s390x": true, "wasm": true,
}

// SetBuildContext overrides the parser's target GOOS/GOARCH for build constraint filtering.
func (p *Parser) SetBuildContext(goos, goarch string) {
	p.buildCtx = &buildContext{
		GOOS:      goos,
		GOARCH:    goarch,
		GoVersion: p.buildCtx.GoVersion,
		tags:      p.buildCtx.tags,
	}
}

// AddBuildTags marks additional build tags as satisfied (like `go build -tags`).
func (p *Parser) AddBuildTags(tags ...string) {
	if p.buildCtx.tags == nil {
		p.buildCtx.tags = map[string]bool{}
	}
	for _, t := range tags {
		if t != "" {
			p.buildCtx.tags[t] = true
		}
	}
}

// MatchFileNameFor reports whether name matches the given GOOS/GOARCH constraints
// encoded in the file name. It is like MatchFileName but for an explicit platform.
func MatchFileNameFor(name, goos, goarch string) bool {
	return MatchFileName(name, &buildContext{
		GOOS:      goos,
		GOARCH:    goarch,
		GoVersion: version.Lang(runtime.Version()),
	})
}

var unixOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"linux": true, "netbsd": true, "openbsd": true, "solaris": true,
}
