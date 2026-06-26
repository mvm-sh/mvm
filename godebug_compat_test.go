package main

import (
	"os"
	"strings"
	"testing"
)

func TestGoModMinor(t *testing.T) {
	cases := []struct {
		mod   string
		minor int
		ok    bool
	}{
		{"module x\n\ngo 1.12\n", 12, true},
		{"module x\ngo 1.24\n", 24, true},
		{"module x\ngo 1.21.5\n", 21, true},
		{"module x\n", 0, false},
		{"module x\ngo 2.0\n", 0, false},
	}
	for _, c := range cases {
		minor, ok := goModMinor([]byte(c.mod))
		if minor != c.minor || ok != c.ok {
			t.Errorf("goModMinor(%q) = (%d,%v), want (%d,%v)", c.mod, minor, ok, c.minor, c.ok)
		}
	}
}

// pre-1.24 modules get randseednop=0; modern modules and explicit user entries
// are left untouched.
func TestApplyModuleGodebug(t *testing.T) {
	cases := []struct {
		name    string
		mod     string
		initEnv string
		wantKV  string // expected randseednop entry, "" means absent
	}{
		{"old module sets compat", "module x\ngo 1.12\n", "", "randseednop=0"},
		{"modern module untouched", "module x\ngo 1.24\n", "", ""},
		{"no go directive untouched", "module x\n", "", ""},
		{"explicit user value wins", "module x\ngo 1.12\n", "randseednop=1", "randseednop=1"},
		{"merges with existing", "module x\ngo 1.12\n", "http2debug=1", "randseednop=0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GODEBUG", c.initEnv)
			applyModuleGodebug([]byte(c.mod))
			got := os.Getenv("GODEBUG")
			has := ""
			for kv := range strings.SplitSeq(got, ",") {
				if strings.HasPrefix(kv, "randseednop=") {
					has = kv
				}
			}
			if has != c.wantKV {
				t.Errorf("GODEBUG=%q: randseednop entry = %q, want %q", got, has, c.wantKV)
			}
			if c.initEnv == "http2debug=1" && !strings.Contains(got, "http2debug=1") {
				t.Errorf("GODEBUG=%q dropped pre-existing http2debug=1", got)
			}
		})
	}
}
