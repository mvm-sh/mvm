package scan

import (
	"fmt"
	"strings"
)

// Source describes a source text.
type Source struct {
	Name    string
	Base    int    // base byte offset in the unified position space
	Len     int    // length in bytes
	content string // source text for line/col resolution
}

// Content returns the source text.
func (s *Source) Content() string { return s.content }

// Lines returns the number of lines in the source. An empty source has
// zero lines; a source whose last character is '\n' is not counted as
// having a trailing empty line.
func (s *Source) Lines() int {
	if s.content == "" {
		return 0
	}
	n := strings.Count(s.content, "\n")
	if !strings.HasSuffix(s.content, "\n") {
		n++
	}
	return n
}

// Sources is an ordered list of Source entries.
type Sources []Source

// Add registers a new source and returns its base offset.
func (ss *Sources) Add(name, src string) int {
	base := 0
	if n := len(*ss); n > 0 {
		last := (*ss)[n-1]
		base = last.Base + last.Len + 1 // +1 for implicit newline separator
	}
	*ss = append(*ss, Source{Name: name, Base: base, Len: len(src), content: src})
	return base
}

// ByName returns the source with the given name, or nil if not found.
// When multiple sources share a name (e.g. the REPL's anonymous chunks),
// the first registered match is returned.
func (ss Sources) ByName(name string) *Source {
	for i := range ss {
		if ss[i].Name == name {
			return &ss[i]
		}
	}
	return nil
}

// find locates the source containing pos and returns it with the
// pos-relative local offset. Returns (nil, 0) when pos is out of range.
func (ss Sources) find(pos int) (*Source, int) {
	if len(ss) == 0 || pos < 0 {
		return nil, 0
	}
	i := len(ss) - 1
	for i > 0 && ss[i].Base > pos {
		i--
	}
	s := &ss[i]
	local := pos - s.Base
	if local < 0 || local > s.Len {
		return nil, 0
	}
	return s, local
}

// Resolve converts a global byte offset to (source name, line, col).
// Returns ("", 0, 0) if pos is out of range.
func (ss Sources) Resolve(pos int) (name string, line, col int) {
	s, local := ss.find(pos)
	if s == nil {
		return "", 0, 0
	}
	line, col = lineCol(s.content, local)
	return s.Name, line, col
}

// FormatPos converts a global byte offset to a "[file:]line:col" string.
func (ss Sources) FormatPos(pos int) string {
	name, line, col := ss.Resolve(pos)
	if name == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d", name, line, col)
}

// LineText returns the source line containing pos, without the trailing
// newline. Returns "" if pos is out of range.
func (ss Sources) LineText(pos int) string {
	s, local := ss.find(pos)
	if s == nil {
		return ""
	}
	start := strings.LastIndexByte(s.content[:local], '\n') + 1
	end := len(s.content)
	if nl := strings.IndexByte(s.content[local:], '\n'); nl >= 0 {
		end = local + nl
	}
	return strings.TrimRight(s.content[start:end], " \t\r")
}

// Snippet renders the source line containing pos plus a caret pointing at
// pos, as
//
//	\n  <line> | <text>\n  <pad>^\n
//
// Returns "" when pos has no resolvable source line. Shared by the runtime
// PanicError renderer (vm) and compile-time diagnostics (interp.Eval).
func (ss Sources) Snippet(pos int) string {
	if len(ss) == 0 || pos == 0 {
		return ""
	}
	_, line, col := ss.Resolve(pos)
	if line == 0 {
		return ""
	}
	text := ss.LineText(pos)
	if text == "" && col == 0 {
		return ""
	}
	// Replace tabs with single spaces so the caret column aligns. col is
	// byte-based (matches LineText byte indexing).
	text = strings.ReplaceAll(text, "\t", " ")
	const maxWidth = 120
	caretCol := col
	if len(text) > maxWidth {
		// Window the line around caretCol if it's far to the right.
		start := 0
		if caretCol > maxWidth-20 {
			start = caretCol - (maxWidth - 20)
		}
		end := min(start+maxWidth, len(text))
		if start > 0 {
			text = "..." + text[start:end]
			caretCol = caretCol - start + 3
		} else {
			text = text[:end] + "..."
		}
	}
	prefix := fmt.Sprintf("  %d | ", line)
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s%s\n", prefix, text)
	if caretCol > 0 {
		b.WriteString(strings.Repeat(" ", len(prefix)+caretCol-1))
		b.WriteString("^\n")
	}
	return b.String()
}

func lineCol(src string, offset int) (line, col int) {
	offset = min(offset, len(src))
	prefix := src[:offset]
	line = 1 + strings.Count(prefix, "\n")
	col = offset - strings.LastIndex(prefix, "\n")
	return line, col
}
