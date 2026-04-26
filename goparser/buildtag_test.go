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
