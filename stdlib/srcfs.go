package stdlib

import _ "embed"

//go:generate go run gen_stdzip.go
//go:generate go run gen_vendorzip.go

//go:embed src.zip
var stdZip []byte

// EmbeddedStd returns the Go-module-proxy-format zip bytes for the std
// module snapshot baked into this binary.
func EmbeddedStd() []byte { return stdZip }

//go:embed xnet.zip
var xnetZip []byte

//go:embed xtext.zip
var xtextZip []byte

// VendorModule names an embedded third-party module shipped so that an
// interpreted package importing it (net/http -> golang.org/x/net,
// golang.org/x/text) resolves offline, like the std mirror.
type VendorModule struct {
	Path    string
	Version string
	Zip     []byte
}

// EmbeddedVendor returns the third-party module zips baked into this binary.
func EmbeddedVendor() []VendorModule {
	return []VendorModule{
		{Path: "golang.org/x/net", Version: "v0.56.0", Zip: xnetZip},
		{Path: "golang.org/x/text", Version: "v0.38.0", Zip: xtextZip},
	}
}
