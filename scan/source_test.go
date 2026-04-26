package scan

import "testing"

func TestSourcesAdd(t *testing.T) {
	var ss Sources

	base0 := ss.Add("first", "abc\ndef")
	if base0 != 0 {
		t.Fatalf("first base = %d, want 0", base0)
	}

	// Second source starts after first (len=7) + 1 gap = 8.
	base1 := ss.Add("second", "ghi\njkl\nmno")
	if want := 8; base1 != want {
		t.Fatalf("second base = %d, want %d", base1, want)
	}

	// Third source starts after second (base=8, len=11) + 1 gap = 20.
	base2 := ss.Add("test.go", "pqr")
	if want := 20; base2 != want {
		t.Fatalf("third base = %d, want %d", base2, want)
	}

	if len(ss) != 3 {
		t.Fatalf("len = %d, want 3", len(ss))
	}
}

func TestSourcesResolveMulti(t *testing.T) {
	var ss Sources
	ss.Add("first", "abc\ndef")  // base=0, len=7
	ss.Add("second", "ghi\njkl") // base=8, len=7

	tests := []struct {
		pos               int
		wantName          string
		wantLine, wantCol int
	}{
		// First source: "abc\ndef"
		{0, "first", 1, 1}, // 'a'
		{3, "first", 1, 4}, // '\n'
		{4, "first", 2, 1}, // 'd'
		{7, "first", 2, 4}, // one past 'f' (end of source)

		// Second source: "ghi\njkl"
		{8, "second", 1, 1},  // 'g'
		{11, "second", 1, 4}, // '\n'
		{12, "second", 2, 1}, // 'j'
		{15, "second", 2, 4}, // one past 'l'
	}
	for _, tt := range tests {
		name, line, col := ss.Resolve(tt.pos)
		if name != tt.wantName || line != tt.wantLine || col != tt.wantCol {
			t.Errorf("Resolve(%d) = (%q, %d, %d), want (%q, %d, %d)",
				tt.pos, name, line, col, tt.wantName, tt.wantLine, tt.wantCol)
		}
	}
}

func TestSourcesResolveOutOfRange(t *testing.T) {
	var ss Sources
	ss.Add("first", "abc")  // base=0, len=3
	ss.Add("second", "def") // base=4, len=3

	tests := []int{-1, -100}
	for _, pos := range tests {
		name, _, _ := ss.Resolve(pos)
		if name != "" {
			t.Errorf("Resolve(%d) name = %q, want empty", pos, name)
		}
	}
}

func TestSourcesResolveEmpty(t *testing.T) {
	var ss Sources
	name, line, col := ss.Resolve(0)
	if name != "" || line != 0 || col != 0 {
		t.Errorf("Resolve on empty Sources = (%q, %d, %d), want (\"\", 0, 0)", name, line, col)
	}
}

func TestSourcesResolveGap(t *testing.T) {
	var ss Sources
	ss.Add("first", "ab")   // base=0, len=2
	ss.Add("second", "cde") // base=3, len=3

	// Position in the gap between sources (offset 2 is end of first, 3 is start of second).
	// Offset 2 is at the end boundary of first source (local=2, Len=2), should resolve.
	name, _, _ := ss.Resolve(2)
	if name != "first" {
		t.Errorf("Resolve(2) name = %q, want %q", name, "first")
	}
}

func TestFormatPosMulti(t *testing.T) {
	var ss Sources
	ss.Add("repl", "x := 1")       // base=0, len=6
	ss.Add("main.go", "func main") // base=7, len=9

	tests := []struct {
		pos  int
		want string
	}{
		{0, "repl:1:1"},     // inline: line:col only
		{5, "repl:1:6"},     // inline: col 6
		{7, "main.go:1:1"},  // file: includes path
		{12, "main.go:1:6"}, // file: col 6
	}
	for _, tt := range tests {
		got := ss.FormatPos(tt.pos)
		if got != tt.want {
			t.Errorf("FormatPos(%d) = %q, want %q", tt.pos, got, tt.want)
		}
	}

	// Out of range returns empty.
	if got := ss.FormatPos(-1); got != "" {
		t.Errorf("FormatPos(-1) = %q, want empty", got)
	}
}

func TestLineCol(t *testing.T) {
	tests := []struct {
		src               string
		offset            int
		wantLine, wantCol int
	}{
		{"", 0, 1, 1},
		{"abc", 0, 1, 1},
		{"abc", 3, 1, 4},
		{"abc\ndef", 4, 2, 1},
		{"abc\ndef", 6, 2, 3},
		{"a\nb\nc", 4, 3, 1},
		// Offset beyond length is clamped.
		{"ab", 10, 1, 3},
	}
	for _, tt := range tests {
		line, col := lineCol(tt.src, tt.offset)
		if line != tt.wantLine || col != tt.wantCol {
			t.Errorf("lineCol(%q, %d) = (%d, %d), want (%d, %d)",
				tt.src, tt.offset, line, col, tt.wantLine, tt.wantCol)
		}
	}
}
