// Code generation directive for stdlib bindings.
// Run "go generate ./stdlib" to regenerate all binding files.
//
// Public stdlib packages intentionally not bound here:
//   - unsafe, plugin, runtime/race: cannot be exposed via reflect bindings
//     (compiler/linker intrinsics, native plugin loader, race-detector
//     internals).
//   - time/tzdata: blank-import side-effect package with no exported symbols.
//   - cmp, iter, maps, slices: bound as interpreted source under stdlib/src/
//     because their APIs are generic.
//   - syscall: generated per-platform by the Makefile (syscall_<os>_<arch>.go).
//
// Stdlib packages that are listed below but currently produce only a stub
// (just "package stdlib") because every exported symbol is generic and so
// cannot be reflect-bound: crypto/hkdf, crypto/pbkdf2, unique, weak. The
// stubs are kept so that bindings appear automatically once these packages
// gain non-generic exports.

package stdlib

//go:generate go run ../cmd/extract -stdlib archive/tar $GOROOT/src/archive/tar
//go:generate go run ../cmd/extract -stdlib archive/zip $GOROOT/src/archive/zip
//go:generate go run ../cmd/extract -stdlib bufio $GOROOT/src/bufio
//go:generate go run ../cmd/extract -stdlib bytes $GOROOT/src/bytes
//go:generate go run ../cmd/extract -stdlib compress/bzip2 $GOROOT/src/compress/bzip2
//go:generate go run ../cmd/extract -stdlib compress/flate $GOROOT/src/compress/flate
//go:generate go run ../cmd/extract -stdlib compress/gzip $GOROOT/src/compress/gzip
//go:generate go run ../cmd/extract -stdlib compress/lzw $GOROOT/src/compress/lzw
//go:generate go run ../cmd/extract -stdlib compress/zlib $GOROOT/src/compress/zlib
//go:generate go run ../cmd/extract -stdlib container/heap $GOROOT/src/container/heap
//go:generate go run ../cmd/extract -stdlib container/list $GOROOT/src/container/list
//go:generate go run ../cmd/extract -stdlib container/ring $GOROOT/src/container/ring
//go:generate go run ../cmd/extract -stdlib context $GOROOT/src/context
//go:generate go run ../cmd/extract -stdlib crypto $GOROOT/src/crypto
//go:generate go run ../cmd/extract -stdlib crypto/aes $GOROOT/src/crypto/aes
//go:generate go run ../cmd/extract -stdlib crypto/cipher $GOROOT/src/crypto/cipher
//go:generate go run ../cmd/extract -stdlib crypto/des $GOROOT/src/crypto/des
//go:generate go run ../cmd/extract -stdlib crypto/dsa $GOROOT/src/crypto/dsa
//go:generate go run ../cmd/extract -stdlib crypto/ecdh $GOROOT/src/crypto/ecdh
//go:generate go run ../cmd/extract -stdlib crypto/ecdsa $GOROOT/src/crypto/ecdsa
//go:generate go run ../cmd/extract -stdlib crypto/ed25519 $GOROOT/src/crypto/ed25519
//go:generate go run ../cmd/extract -stdlib crypto/elliptic $GOROOT/src/crypto/elliptic
//go:generate go run ../cmd/extract -stdlib crypto/fips140 $GOROOT/src/crypto/fips140
//go:generate go run ../cmd/extract -stdlib crypto/hkdf $GOROOT/src/crypto/hkdf
//go:generate go run ../cmd/extract -stdlib crypto/hmac $GOROOT/src/crypto/hmac
//go:generate go run ../cmd/extract -stdlib crypto/hpke $GOROOT/src/crypto/hpke
//go:generate go run ../cmd/extract -stdlib crypto/md5 $GOROOT/src/crypto/md5
//go:generate go run ../cmd/extract -stdlib crypto/mlkem $GOROOT/src/crypto/mlkem
//go:generate go run ../cmd/extract -stdlib crypto/pbkdf2 $GOROOT/src/crypto/pbkdf2
//go:generate go run ../cmd/extract -stdlib crypto/rand $GOROOT/src/crypto/rand
//go:generate go run ../cmd/extract -stdlib crypto/rc4 $GOROOT/src/crypto/rc4
//go:generate go run ../cmd/extract -stdlib crypto/rsa $GOROOT/src/crypto/rsa
//go:generate go run ../cmd/extract -stdlib crypto/sha1 $GOROOT/src/crypto/sha1
//go:generate go run ../cmd/extract -stdlib crypto/sha256 $GOROOT/src/crypto/sha256
//go:generate go run ../cmd/extract -stdlib crypto/sha3 $GOROOT/src/crypto/sha3
//go:generate go run ../cmd/extract -stdlib crypto/sha512 $GOROOT/src/crypto/sha512
//go:generate go run ../cmd/extract -stdlib crypto/subtle $GOROOT/src/crypto/subtle
//go:generate go run ../cmd/extract -stdlib crypto/tls $GOROOT/src/crypto/tls
//go:generate go run ../cmd/extract -stdlib crypto/x509 $GOROOT/src/crypto/x509
//go:generate go run ../cmd/extract -stdlib crypto/x509/pkix $GOROOT/src/crypto/x509/pkix
//go:generate go run ../cmd/extract -stdlib database/sql $GOROOT/src/database/sql
//go:generate go run ../cmd/extract -stdlib database/sql/driver $GOROOT/src/database/sql/driver
//go:generate go run ../cmd/extract -stdlib debug/dwarf $GOROOT/src/debug/dwarf
//go:generate go run ../cmd/extract -stdlib debug/elf $GOROOT/src/debug/elf
//go:generate go run ../cmd/extract -stdlib debug/gosym $GOROOT/src/debug/gosym
//go:generate go run ../cmd/extract -stdlib debug/macho $GOROOT/src/debug/macho
//go:generate go run ../cmd/extract -stdlib debug/buildinfo $GOROOT/src/debug/buildinfo
//go:generate go run ../cmd/extract -stdlib debug/pe $GOROOT/src/debug/pe
//go:generate go run ../cmd/extract -stdlib debug/plan9obj $GOROOT/src/debug/plan9obj
//go:generate go run ../cmd/extract -stdlib embed $GOROOT/src/embed
//go:generate go run ../cmd/extract -stdlib encoding $GOROOT/src/encoding
//go:generate go run ../cmd/extract -stdlib encoding/ascii85 $GOROOT/src/encoding/ascii85
//go:generate go run ../cmd/extract -stdlib encoding/asn1 $GOROOT/src/encoding/asn1
//go:generate go run ../cmd/extract -stdlib encoding/base32 $GOROOT/src/encoding/base32
//go:generate go run ../cmd/extract -stdlib encoding/base64 $GOROOT/src/encoding/base64
//go:generate go run ../cmd/extract -stdlib encoding/binary $GOROOT/src/encoding/binary
//go:generate go run ../cmd/extract -stdlib encoding/csv $GOROOT/src/encoding/csv
//go:generate go run ../cmd/extract -stdlib encoding/gob $GOROOT/src/encoding/gob
//go:generate go run ../cmd/extract -stdlib encoding/hex $GOROOT/src/encoding/hex
//go:generate go run ../cmd/extract -stdlib encoding/json $GOROOT/src/encoding/json
//go:generate go run ../cmd/extract -stdlib encoding/pem $GOROOT/src/encoding/pem
//go:generate go run ../cmd/extract -stdlib encoding/xml $GOROOT/src/encoding/xml
//go:generate go run ../cmd/extract -stdlib errors $GOROOT/src/errors
//go:generate go run ../cmd/extract -stdlib expvar $GOROOT/src/expvar
//go:generate go run ../cmd/extract -stdlib flag $GOROOT/src/flag
//go:generate go run ../cmd/extract -stdlib fmt $GOROOT/src/fmt
//go:generate go run ../cmd/extract -stdlib go/ast $GOROOT/src/go/ast
//go:generate go run ../cmd/extract -stdlib go/build $GOROOT/src/go/build
//go:generate go run ../cmd/extract -stdlib go/build/constraint $GOROOT/src/go/build/constraint
//go:generate go run ../cmd/extract -stdlib go/constant $GOROOT/src/go/constant
//go:generate go run ../cmd/extract -stdlib go/doc $GOROOT/src/go/doc
//go:generate go run ../cmd/extract -stdlib go/doc/comment $GOROOT/src/go/doc/comment
//go:generate go run ../cmd/extract -stdlib go/format $GOROOT/src/go/format
//go:generate go run ../cmd/extract -stdlib go/parser $GOROOT/src/go/parser
//go:generate go run ../cmd/extract -stdlib go/printer $GOROOT/src/go/printer
//go:generate go run ../cmd/extract -stdlib go/scanner $GOROOT/src/go/scanner
//go:generate go run ../cmd/extract -stdlib go/token $GOROOT/src/go/token
//go:generate go run ../cmd/extract -stdlib go/types $GOROOT/src/go/types
//go:generate go run ../cmd/extract -stdlib go/importer $GOROOT/src/go/importer
//go:generate go run ../cmd/extract -stdlib go/version $GOROOT/src/go/version
//go:generate go run ../cmd/extract -stdlib hash $GOROOT/src/hash
//go:generate go run ../cmd/extract -stdlib hash/adler32 $GOROOT/src/hash/adler32
//go:generate go run ../cmd/extract -stdlib hash/crc32 $GOROOT/src/hash/crc32
//go:generate go run ../cmd/extract -stdlib hash/crc64 $GOROOT/src/hash/crc64
//go:generate go run ../cmd/extract -stdlib hash/fnv $GOROOT/src/hash/fnv
//go:generate go run ../cmd/extract -stdlib hash/maphash $GOROOT/src/hash/maphash
//go:generate go run ../cmd/extract -stdlib html $GOROOT/src/html
//go:generate go run ../cmd/extract -stdlib html/template $GOROOT/src/html/template
//go:generate go run ../cmd/extract -stdlib image $GOROOT/src/image
//go:generate go run ../cmd/extract -stdlib image/color $GOROOT/src/image/color
//go:generate go run ../cmd/extract -stdlib image/color/palette $GOROOT/src/image/color/palette
//go:generate go run ../cmd/extract -stdlib image/draw $GOROOT/src/image/draw
//go:generate go run ../cmd/extract -stdlib image/gif $GOROOT/src/image/gif
//go:generate go run ../cmd/extract -stdlib image/jpeg $GOROOT/src/image/jpeg
//go:generate go run ../cmd/extract -stdlib image/png $GOROOT/src/image/png
//go:generate go run ../cmd/extract -stdlib index/suffixarray $GOROOT/src/index/suffixarray
//go:generate go run ../cmd/extract -stdlib io $GOROOT/src/io
//go:generate go run ../cmd/extract -stdlib io/fs $GOROOT/src/io/fs
//go:generate go run ../cmd/extract -stdlib io/ioutil $GOROOT/src/io/ioutil
//go:generate go run ../cmd/extract -stdlib log $GOROOT/src/log
//go:generate go run ../cmd/extract -stdlib log/slog $GOROOT/src/log/slog
//go:generate go run ../cmd/extract -stdlib log/syslog $GOROOT/src/log/syslog
//go:generate go run ../cmd/extract -stdlib math $GOROOT/src/math
//go:generate go run ../cmd/extract -stdlib math/big $GOROOT/src/math/big
//go:generate go run ../cmd/extract -stdlib math/bits $GOROOT/src/math/bits
//go:generate go run ../cmd/extract -stdlib math/cmplx $GOROOT/src/math/cmplx
//go:generate go run ../cmd/extract -stdlib math/rand $GOROOT/src/math/rand
//go:generate go run ../cmd/extract -stdlib math/rand/v2 $GOROOT/src/math/rand/v2
//go:generate go run ../cmd/extract -stdlib mime $GOROOT/src/mime
//go:generate go run ../cmd/extract -stdlib mime/multipart $GOROOT/src/mime/multipart
//go:generate go run ../cmd/extract -stdlib mime/quotedprintable $GOROOT/src/mime/quotedprintable
//go:generate go run ../cmd/extract -stdlib net $GOROOT/src/net
//go:generate go run ../cmd/extract -stdlib net/http $GOROOT/src/net/http
//go:generate go run ../cmd/extract -stdlib net/http/cgi $GOROOT/src/net/http/cgi
//go:generate go run ../cmd/extract -stdlib net/http/cookiejar $GOROOT/src/net/http/cookiejar
//go:generate go run ../cmd/extract -stdlib net/http/fcgi $GOROOT/src/net/http/fcgi
//go:generate go run ../cmd/extract -stdlib net/http/httptest $GOROOT/src/net/http/httptest
//go:generate go run ../cmd/extract -stdlib net/http/httptrace $GOROOT/src/net/http/httptrace
//go:generate go run ../cmd/extract -stdlib net/http/httputil $GOROOT/src/net/http/httputil
//go:generate go run ../cmd/extract -stdlib net/http/pprof $GOROOT/src/net/http/pprof
//go:generate go run ../cmd/extract -stdlib net/mail $GOROOT/src/net/mail
//go:generate go run ../cmd/extract -stdlib net/netip $GOROOT/src/net/netip
//go:generate go run ../cmd/extract -stdlib net/rpc $GOROOT/src/net/rpc
//go:generate go run ../cmd/extract -stdlib net/rpc/jsonrpc $GOROOT/src/net/rpc/jsonrpc
//go:generate go run ../cmd/extract -stdlib net/smtp $GOROOT/src/net/smtp
//go:generate go run ../cmd/extract -stdlib net/textproto $GOROOT/src/net/textproto
//go:generate go run ../cmd/extract -stdlib net/url $GOROOT/src/net/url
//go:generate go run ../cmd/extract -stdlib os $GOROOT/src/os
//go:generate go run ../cmd/extract -stdlib os/exec $GOROOT/src/os/exec
//go:generate go run ../cmd/extract -stdlib os/signal $GOROOT/src/os/signal
//go:generate go run ../cmd/extract -stdlib os/user $GOROOT/src/os/user
//go:generate go run ../cmd/extract -stdlib path $GOROOT/src/path
//go:generate go run ../cmd/extract -stdlib path/filepath $GOROOT/src/path/filepath
//go:generate go run ../cmd/extract -stdlib reflect $GOROOT/src/reflect
//go:generate go run ../cmd/extract -stdlib regexp $GOROOT/src/regexp
//go:generate go run ../cmd/extract -stdlib regexp/syntax $GOROOT/src/regexp/syntax
//go:generate go run ../cmd/extract -stdlib runtime $GOROOT/src/runtime
//go:generate go run ../cmd/extract -stdlib runtime/cgo $GOROOT/src/runtime/cgo
//go:generate go run ../cmd/extract -stdlib runtime/coverage $GOROOT/src/runtime/coverage
//go:generate go run ../cmd/extract -stdlib runtime/debug $GOROOT/src/runtime/debug
//go:generate go run ../cmd/extract -stdlib runtime/metrics $GOROOT/src/runtime/metrics
//go:generate go run ../cmd/extract -stdlib runtime/pprof $GOROOT/src/runtime/pprof
//go:generate go run ../cmd/extract -stdlib runtime/trace $GOROOT/src/runtime/trace
//go:generate go run ../cmd/extract -stdlib sort $GOROOT/src/sort
//go:generate go run ../cmd/extract -stdlib strconv $GOROOT/src/strconv
//go:generate go run ../cmd/extract -stdlib strings $GOROOT/src/strings
//go:generate go run ../cmd/extract -stdlib structs $GOROOT/src/structs
//go:generate go run ../cmd/extract -stdlib sync $GOROOT/src/sync
//go:generate go run ../cmd/extract -stdlib sync/atomic $GOROOT/src/sync/atomic
//go:generate go run ../cmd/extract -stdlib testing $GOROOT/src/testing
//go:generate go run ../cmd/extract -stdlib testing/cryptotest $GOROOT/src/testing/cryptotest
//go:generate go run ../cmd/extract -stdlib testing/fstest $GOROOT/src/testing/fstest
//go:generate go run ../cmd/extract -stdlib testing/iotest $GOROOT/src/testing/iotest
//go:generate go run ../cmd/extract -stdlib testing/quick $GOROOT/src/testing/quick
//go:generate go run ../cmd/extract -stdlib testing/slogtest $GOROOT/src/testing/slogtest
//go:generate go run ../cmd/extract -stdlib testing/synctest $GOROOT/src/testing/synctest
//go:generate go run ../cmd/extract -stdlib text/scanner $GOROOT/src/text/scanner
//go:generate go run ../cmd/extract -stdlib text/tabwriter $GOROOT/src/text/tabwriter
//go:generate go run ../cmd/extract -stdlib text/template $GOROOT/src/text/template
//go:generate go run ../cmd/extract -stdlib text/template/parse $GOROOT/src/text/template/parse
//go:generate go run ../cmd/extract -stdlib time $GOROOT/src/time
//go:generate go run ../cmd/extract -stdlib unicode $GOROOT/src/unicode
//go:generate go run ../cmd/extract -stdlib unicode/utf16 $GOROOT/src/unicode/utf16
//go:generate go run ../cmd/extract -stdlib unicode/utf8 $GOROOT/src/unicode/utf8
//go:generate go run ../cmd/extract -stdlib unique $GOROOT/src/unique
//go:generate go run ../cmd/extract -stdlib weak $GOROOT/src/weak
