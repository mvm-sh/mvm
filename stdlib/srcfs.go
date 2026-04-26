package stdlib

import (
	"embed"
	"io/fs"
)

//go:embed src
var srcRoot embed.FS

// SrcFS returns a filesystem rooted at the embedded stdlib source tree.
// A package "cmp" is found at "cmp/cmp.go" and so on - matching the layout
// expected by goparser.Parser.ParseAll when fed an import path.
func SrcFS() fs.FS {
	sub, err := fs.Sub(srcRoot, "src")
	if err != nil {
		panic(err)
	}
	return sub
}
