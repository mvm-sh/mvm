package goparser

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode"
)

// EmbedBytes returns the bytes recorded for a //go:embed var by its canonical key.
func (p *Parser) EmbedBytes(symKey string) ([]byte, bool) {
	b, ok := p.embeds[symKey]
	return b, ok
}

// scanEmbeds reads the file named by each single-file //go:embed directive in src
// (relative to dir) and records it by var key for allocGlobalSlots. The var type may
// be string, []byte or a named variant (e.g. uint40String); only globs, multi-pattern
// and embed.FS directives are skipped. The scan is line-based.
func (p *Parser) scanEmbeds(fsys fs.FS, dir, src string) {
	if !strings.Contains(src, "//go:embed") {
		return // fast path: most files have no directive; skip the line scan
	}
	var pending []string
	for raw := range strings.SplitSeq(src, "\n") {
		line := strings.TrimSpace(raw)
		if rest, ok := cutEmbedDirective(line); ok {
			pending = append(pending, embedPatterns(rest)...)
			continue
		}
		if len(pending) == 0 {
			continue
		}
		// Only blank lines and //-comments may separate the directive from the var.
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if name, typ := embedVarLine(line); name != "" {
			p.recordEmbed(fsys, dir, pending, name, typ)
		}
		pending = nil
	}
}

// cutEmbedDirective returns the pattern text of a //go:embed directive line.
// The byte after "//go:embed" must be space, so "//go:embedfoo" does not match.
func cutEmbedDirective(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "//go:embed")
	if !ok {
		return "", false
	}
	if rest == "" {
		return "", true
	}
	if !unicode.IsSpace(rune(rest[0])) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// embedPatterns splits pattern text on whitespace, honoring quoted patterns.
func embedPatterns(s string) []string {
	var out []string
	for {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		if s == "" {
			return out
		}
		if q := s[0]; q == '"' || q == '`' {
			if i := strings.IndexByte(s[1:], q); i >= 0 {
				out = append(out, s[1:1+i])
				s = s[1+i+1:]
				continue
			}
			return append(out, s[1:]) // unterminated: take the rest
		}
		if i := strings.IndexFunc(s, unicode.IsSpace); i >= 0 {
			out = append(out, s[:i])
			s = s[i:]
			continue
		}
		return append(out, s)
	}
}

// embedVarLine returns the name and type from a "var Name Type" line, else "", "".
func embedVarLine(line string) (name, typ string) {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "var")
	if !ok || rest == "" || !unicode.IsSpace(rune(rest[0])) {
		return "", ""
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return "", ""
	}
	return fields[0], fields[1] // []byte, string, named types and embed.FS are single tokens
}

func (p *Parser) recordEmbed(fsys fs.FS, dir string, patterns []string, name, typ string) {
	if len(patterns) != 1 || strings.ContainsAny(patterns[0], "*?[") {
		return // globs / multiple patterns imply embed.FS, unsupported
	}
	if typ == "embed.FS" || strings.HasSuffix(typ, ".FS") {
		return // embed.FS unsupported; string/[]byte and their named variants are recorded
	}
	data, err := fs.ReadFile(fsys, path.Join(dir, patterns[0]))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mvm: //go:embed %s: %v\n", patterns[0], err)
		return
	}
	if p.embeds == nil {
		p.embeds = map[string][]byte{}
	}
	p.embeds[p.pkgKey(name)] = data
}
