package goparser

import (
	"reflect"
	"testing"
)

func TestEmbedPatterns(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"data.bin", []string{"data.bin"}},
		{"a.txt b.txt  c.txt", []string{"a.txt", "b.txt", "c.txt"}},
		{`"with space.txt" plain`, []string{"with space.txt", "plain"}},
		{"`back tick.txt`", []string{"back tick.txt"}},
		{"", nil},
	}
	for _, c := range cases {
		if got := embedPatterns(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("embedPatterns(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCutEmbedDirective(t *testing.T) {
	cases := []struct {
		line     string
		wantRest string
		wantOK   bool
	}{
		{"//go:embed data.bin", "data.bin", true},
		{"//go:embed   a b", "a b", true},
		{"//go:embed", "", true},
		{"//go:embedfoo", "", false}, // no separating space
		{"//go:build linux", "", false},
		{"// just a comment", "", false},
	}
	for _, c := range cases {
		rest, ok := cutEmbedDirective(c.line)
		if ok != c.wantOK || rest != c.wantRest {
			t.Errorf("cutEmbedDirective(%q) = (%q, %v), want (%q, %v)", c.line, rest, ok, c.wantRest, c.wantOK)
		}
	}
}

func TestEmbedVarLine(t *testing.T) {
	cases := []struct {
		line     string
		wantName string
		wantTyp  string
	}{
		{"var Blob []byte", "Blob", "[]byte"},
		{"var Greeting string", "Greeting", "string"},
		{"var X []byte // trailing", "X", "[]byte"},
		{"var (", "", ""},
		{"func F() {}", "", ""},
		{"variable := 1", "", ""}, // "var" not followed by space
	}
	for _, c := range cases {
		name, typ := embedVarLine(c.line)
		if name != c.wantName || typ != c.wantTyp {
			t.Errorf("embedVarLine(%q) = (%q, %q), want (%q, %q)", c.line, name, typ, c.wantName, c.wantTyp)
		}
	}
}
