package goparser

import "testing"

func TestMatchFileName(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		goarch string
		want   bool
	}{
		{"foo.go", "linux", "amd64", true},
		{"foo_linux.go", "linux", "amd64", true},
		{"foo_linux.go", "darwin", "amd64", false},
		{"foo_amd64.go", "linux", "amd64", true},
		{"foo_amd64.go", "linux", "arm64", false},
		{"foo_linux_amd64.go", "linux", "amd64", true},
		{"foo_linux_amd64.go", "linux", "arm64", false},
		{"foo_linux_amd64.go", "darwin", "amd64", false},
		{"foo_windows.go", "linux", "amd64", false},
		{"linux.go", "darwin", "arm64", true},        // no underscore prefix
		{"foo_bar_linux.go", "linux", "amd64", true}, // multiple segments
		{"foo_bar_linux.go", "darwin", "amd64", false},
		{"foo_other.go", "linux", "amd64", true}, // unknown tag

		// _test.go marker must be stripped before reading the GOOS/GOARCH suffix,
		// otherwise "test" shadows the constraint and the file is never filtered.
		{"arith_s390x_test.go", "linux", "amd64", false}, // s390x test on amd64
		{"arith_s390x_test.go", "linux", "arm64", false}, // s390x test on arm64
		{"arith_s390x_test.go", "linux", "s390x", true},  // s390x test on s390x
		{"foo_linux_test.go", "linux", "amd64", true},    // GOOS test, matching
		{"foo_linux_test.go", "darwin", "amd64", false},  // GOOS test, mismatch
		{"foo_linux_amd64_test.go", "linux", "amd64", true},
		{"foo_linux_amd64_test.go", "linux", "arm64", false},
		{"export_test.go", "linux", "amd64", true}, // plain test file, no constraint
		{"foo_test.go", "linux", "amd64", true},    // plain test file, no constraint
	}
	for _, tt := range tests {
		t.Run(tt.name+"_"+tt.goos+"_"+tt.goarch, func(t *testing.T) {
			ctx := &buildContext{GOOS: tt.goos, GOARCH: tt.goarch, GoVersion: "go1.24"}
			if got := MatchFileName(tt.name, ctx); got != tt.want {
				t.Errorf("MatchFileName(%q, %s/%s) = %v, want %v", tt.name, tt.goos, tt.goarch, got, tt.want)
			}
		})
	}
}

func TestMatchBuildDirective(t *testing.T) {
	tests := []struct {
		desc    string
		content string
		goos    string
		goarch  string
		version string
		want    bool
	}{
		{"no directive", "package main\n", "linux", "amd64", "go1.24", true},
		{"linux on linux", "//go:build linux\n\npackage main\n", "linux", "amd64", "go1.24", true},
		{"linux on darwin", "//go:build linux\n\npackage main\n", "darwin", "amd64", "go1.24", false},
		{"linux or darwin", "//go:build linux || darwin\n\npackage main\n", "darwin", "arm64", "go1.24", true},
		{"not windows on linux", "//go:build !windows\n\npackage main\n", "linux", "amd64", "go1.24", true},
		{"not windows on windows", "//go:build !windows\n\npackage main\n", "windows", "amd64", "go1.24", false},
		{"go version match", "//go:build go1.21\n\npackage main\n", "linux", "amd64", "go1.24", true},
		{"go version too new", "//go:build go1.25\n\npackage main\n", "linux", "amd64", "go1.24", false},
		{"ignore", "//go:build ignore\n\npackage main\n", "linux", "amd64", "go1.24", false},
		{"comment before directive", "// Package foo does stuff.\n//go:build linux\n\npackage main\n", "linux", "amd64", "go1.24", true},
		{"comment before directive wrong os", "// Package foo does stuff.\n//go:build linux\n\npackage main\n", "darwin", "amd64", "go1.24", false},
		{"arch constraint", "//go:build arm64\n\npackage main\n", "linux", "arm64", "go1.24", true},
		{"arch constraint mismatch", "//go:build arm64\n\npackage main\n", "linux", "amd64", "go1.24", false},
		{"compound", "//go:build linux && amd64\n\npackage main\n", "linux", "amd64", "go1.24", true},
		{"compound mismatch", "//go:build linux && arm64\n\npackage main\n", "linux", "amd64", "go1.24", false},

		// Legacy `// +build` directives (still used by older modules, e.g.
		// github.com/google/uuid v1.6.0's node_js.go / node_net.go).
		{"plus js on js", "// +build js\n\npackage uuid\n", "js", "wasm", "go1.24", true},
		{"plus js on darwin", "// +build js\n\npackage uuid\n", "darwin", "amd64", "go1.24", false},
		{"plus !js on js", "// +build !js\n\npackage uuid\n", "js", "wasm", "go1.24", false},
		{"plus !js on darwin", "// +build !js\n\npackage uuid\n", "darwin", "amd64", "go1.24", true},
		{"plus space-OR", "// +build linux darwin\n\npackage main\n", "darwin", "amd64", "go1.24", true},
		{"plus comma-AND match", "// +build linux,amd64\n\npackage main\n", "linux", "amd64", "go1.24", true},
		{"plus comma-AND mismatch", "// +build linux,amd64\n\npackage main\n", "linux", "arm64", "go1.24", false},
		{"plus multi-line AND", "// +build linux\n// +build amd64\n\npackage main\n", "linux", "amd64", "go1.24", true},
		{"plus multi-line AND mismatch", "// +build linux\n// +build amd64\n\npackage main\n", "linux", "arm64", "go1.24", false},
		{"gobuild overrides plus", "//go:build linux\n// +build darwin\n\npackage main\n", "linux", "amd64", "go1.24", true},

		// mvm enables the pure-Go fallback tags (go-spew's bypass.go/bypasssafe.go,
		// x/crypto purego variants).
		{"safe tag on", "//go:build safe\n\npackage spew\n", "linux", "amd64", "go1.24", true},
		{"not-safe excluded", "// +build !js,!appengine,!safe,!disableunsafe,go1.4\n\npackage spew\n", "linux", "amd64", "go1.24", false},
		{"plus safe-OR included", "// +build js appengine safe disableunsafe !go1.4\n\npackage spew\n", "linux", "amd64", "go1.24", true},
		{"purego tag on", "//go:build purego\n\npackage x\n", "linux", "amd64", "go1.24", true},
		{"not-purego excluded", "//go:build !purego\n\npackage x\n", "linux", "amd64", "go1.24", false},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			ctx := &buildContext{GOOS: tt.goos, GOARCH: tt.goarch, GoVersion: tt.version}
			if got := matchBuildDirective(tt.content, ctx); got != tt.want {
				t.Errorf("matchBuildDirective(%q) = %v, want %v", tt.desc, got, tt.want)
			}
		})
	}
}

func TestMatchTag(t *testing.T) {
	ctx := &buildContext{GOOS: "linux", GOARCH: "amd64", GoVersion: "go1.24"}

	tests := []struct {
		tag  string
		want bool
	}{
		{"linux", true},
		{"amd64", true},
		{"darwin", false},
		{"arm64", false},
		{"unix", true},
		{"go1.21", true},
		{"go1.24", true},
		{"go1.25", false},
		{"cgo", false},
		{"ignore", false},
		{"something", false},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			if got := ctx.matchTag(tt.tag); got != tt.want {
				t.Errorf("matchTag(%q) = %v, want %v", tt.tag, got, tt.want)
			}
		})
	}
}

func TestMatchTagUnix(t *testing.T) {
	for _, os := range []string{"darwin", "freebsd", "openbsd", "netbsd"} {
		ctx := &buildContext{GOOS: os, GOARCH: "amd64", GoVersion: "go1.24"}
		if !ctx.matchTag("unix") {
			t.Errorf("matchTag(\"unix\") = false for GOOS=%s, want true", os)
		}
	}
	ctx := &buildContext{GOOS: "windows", GOARCH: "amd64", GoVersion: "go1.24"}
	if ctx.matchTag("unix") {
		t.Error("matchTag(\"unix\") = true for GOOS=windows, want false")
	}
}

// TestBuildTags covers the satisfied-tag set: defaults (purego/safe/
// nethttpomithttp2) plus AddBuildTags, all evaluated through //go:build.
func TestBuildTags(t *testing.T) {
	dir := func(src string, ctx *buildContext) bool { return matchBuildDirective(src, ctx) }

	def := defaultBuildContext()
	cases := []struct {
		desc string
		src  string
		ctx  *buildContext
		want bool
	}{
		{"default purego", "//go:build purego\n\npackage p", def, true},
		{"default nethttpomithttp2", "//go:build nethttpomithttp2\n\npackage p", def, true},
		{"its negation excluded", "//go:build !nethttpomithttp2\n\npackage p", def, false},
		{"unknown tag false", "//go:build sqlite_omit_load_extension\n\npackage p", def, false},
		{"GOOS still works", "//go:build wasip1\n\npackage p", &buildContext{GOOS: "wasip1", GOARCH: "wasm", GoVersion: "go1.26", tags: defaultTags()}, true},
	}
	for _, c := range cases {
		if got := dir(c.src, c.ctx); got != c.want {
			t.Errorf("%s: matchBuildDirective = %v, want %v", c.desc, got, c.want)
		}
	}

	// AddBuildTags marks an otherwise-unknown tag as satisfied (like -tags).
	p := &Parser{buildCtx: defaultBuildContext()}
	const src = "//go:build sqlite_omit_load_extension\n\npackage p"
	if matchBuildDirective(src, p.buildCtx) {
		t.Fatal("tag satisfied before AddBuildTags")
	}
	p.AddBuildTags("sqlite_omit_load_extension")
	if !matchBuildDirective(src, p.buildCtx) {
		t.Error("tag not satisfied after AddBuildTags")
	}
}
