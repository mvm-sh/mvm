package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// TestRemoteGoEmbed verifies //go:embed of a single sibling file into a
// package-level []byte and a string var: the parser reads the file from the
// package FS and installs its bytes as the var's initial value. Before embed
// support both vars were empty, which is why protobuf's editiondefaults
// panicked "unsupported edition" (its embedded Defaults was nil).
func TestRemoteGoEmbed(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/assets",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod":    "module example.com/x/assets\n",
			"data.bin":  "hello\x00\x01\x02bytes", // 13 bytes incl. NUL
			"greet.txt": "hi there",
			"assets.go": `package assets

import _ "embed"

//go:embed data.bin
var Blob []byte

//go:embed greet.txt
var Greeting string

func BlobLen() int  { return len(Blob) }
func Greet() string { return Greeting }
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/assets"
println(assets.BlobLen(), assets.Greet())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "13 hi there\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
