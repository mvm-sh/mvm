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
	GoVersion string // major.minor only, e.g. "go1.24"
}

func defaultBuildContext() *buildContext {
	return &buildContext{
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		GoVersion: version.Lang(runtime.Version()),
	}
}

// MatchFileName reports whether name (a .go file basename) matches the
// given build context's GOOS/GOARCH constraints encoded in the file name.
func MatchFileName(name string, ctx *buildContext) bool {
	if ctx == nil {
		ctx = defaultBuildContext()
	}
	name = strings.TrimSuffix(name, ".go")

	i := strings.Index(name, "_")
	if i < 0 {
		return true // no underscore, no constraint
	}
	tags := strings.Split(name[i+1:], "_")

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
	for src != "" {
		var line string
		if i := strings.IndexByte(src, '\n'); i >= 0 {
			line, src = src[:i], src[i+1:]
		} else {
			line, src = src, ""
		}
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			return true // reached non-comment line (e.g. package), no constraint found
		}
		if constraint.IsGoBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				return false
			}
			return expr.Eval(ctx.matchTag)
		}
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
	return false
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
