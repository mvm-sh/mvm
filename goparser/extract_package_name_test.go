package goparser

import "testing"

// extractPackageName must skip a /* */ license-header block comment before the
// package clause; returning "" there leaves mainPkg empty, which silently
// disables the internal/external test split (grpc/protobuf headers are blocks).
func TestExtractPackageName(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"plain", "package status\n", "status"},
		{"external", "package status_test\n", "status_test"},
		{"line comment", "// Copyright\npackage status\n", "status"},
		{"build tag", "//go:build linux\n\npackage status\n", "status"},
		{"block header", "/*\n * Copyright 2017\n */\n\npackage status\n", "status"},
		{"block then line", "/* c */\n// more\npackage foo\n", "foo"},
		{"no package", "/* just a comment */\n", ""},
		{"unterminated block", "/* never ends\npackage foo\n", ""},
	}
	for _, c := range cases {
		if got := extractPackageName(c.src); got != c.want {
			t.Errorf("%s: extractPackageName(%q) = %q, want %q", c.name, c.src, got, c.want)
		}
	}
}
