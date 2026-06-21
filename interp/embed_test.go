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

// TestRemoteGoEmbedNamedType verifies //go:embed into a NAMED string type with a
// value-receiver method (golang.org/x/net/publicsuffix's uint40String pattern).
// Two bugs were exercised: (1) the embed scanner rejected named string/[]byte types,
// leaving the var empty; (2) the embed materialized the var's rtype before its methods
// were registered, reserving a method-less identity that broke ptr-method attach.
func TestRemoteGoEmbedNamedType(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/table",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod":    "module example.com/x/table\n",
			"nodes.bin": "\x00\x00\x00\x00\x01\x00\x00\x00\x00\x02", // two 5-byte nodes
			"table.go": `package table

import _ "embed"

type uint40String string

func (u uint40String) get(i uint32) uint64 {
	off := uint64(i * 5)
	u = u[off:]
	return uint64(u[4]) | uint64(u[3])<<8 | uint64(u[2])<<16 | uint64(u[1])<<24 | uint64(u[0])<<32
}

//go:embed nodes.bin
var nodes uint40String

func Node(i uint32) uint64 { return nodes.get(i) }
func Len() int            { return len(nodes) }
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/table"
println(table.Len(), table.Node(0), table.Node(1))`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "10 1 2\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
