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

//go:generate extract -stdlib archive/tar $GOROOT/src/archive/tar
//go:generate extract -stdlib archive/zip $GOROOT/src/archive/zip
//go:generate extract -stdlib bufio $GOROOT/src/bufio
//go:generate extract -stdlib bytes $GOROOT/src/bytes
//go:generate extract -stdlib compress/bzip2 $GOROOT/src/compress/bzip2
//go:generate extract -stdlib compress/flate $GOROOT/src/compress/flate
//go:generate extract -stdlib compress/gzip $GOROOT/src/compress/gzip
//go:generate extract -stdlib compress/lzw $GOROOT/src/compress/lzw
//go:generate extract -stdlib compress/zlib $GOROOT/src/compress/zlib
//go:generate extract -stdlib container/heap $GOROOT/src/container/heap
//go:generate extract -stdlib container/list $GOROOT/src/container/list
//go:generate extract -stdlib container/ring $GOROOT/src/container/ring
//go:generate extract -stdlib context $GOROOT/src/context
//go:generate extract -stdlib crypto $GOROOT/src/crypto
//go:generate extract -stdlib crypto/aes $GOROOT/src/crypto/aes
//go:generate extract -stdlib crypto/cipher $GOROOT/src/crypto/cipher
//go:generate extract -stdlib crypto/des $GOROOT/src/crypto/des
//go:generate extract -stdlib crypto/dsa $GOROOT/src/crypto/dsa
//go:generate extract -stdlib crypto/ecdh $GOROOT/src/crypto/ecdh
//go:generate extract -stdlib crypto/ecdsa $GOROOT/src/crypto/ecdsa
//go:generate extract -stdlib crypto/ed25519 $GOROOT/src/crypto/ed25519
//go:generate extract -stdlib crypto/elliptic $GOROOT/src/crypto/elliptic
//go:generate extract -stdlib crypto/fips140 $GOROOT/src/crypto/fips140
//go:generate extract -stdlib crypto/hkdf $GOROOT/src/crypto/hkdf
//go:generate extract -stdlib crypto/hmac $GOROOT/src/crypto/hmac
//go:generate extract -stdlib crypto/hpke $GOROOT/src/crypto/hpke
//go:generate extract -stdlib crypto/md5 $GOROOT/src/crypto/md5
//go:generate extract -stdlib crypto/mlkem $GOROOT/src/crypto/mlkem
//go:generate extract -stdlib crypto/pbkdf2 $GOROOT/src/crypto/pbkdf2
//go:generate extract -stdlib crypto/rand $GOROOT/src/crypto/rand
//go:generate extract -stdlib crypto/rc4 $GOROOT/src/crypto/rc4
//go:generate extract -stdlib crypto/rsa $GOROOT/src/crypto/rsa
//go:generate extract -stdlib crypto/sha1 $GOROOT/src/crypto/sha1
//go:generate extract -stdlib crypto/sha256 $GOROOT/src/crypto/sha256
//go:generate extract -stdlib crypto/sha3 $GOROOT/src/crypto/sha3
//go:generate extract -stdlib crypto/sha512 $GOROOT/src/crypto/sha512
//go:generate extract -stdlib crypto/subtle $GOROOT/src/crypto/subtle
//go:generate extract -stdlib crypto/tls $GOROOT/src/crypto/tls
//go:generate extract -stdlib crypto/x509 $GOROOT/src/crypto/x509
//go:generate extract -stdlib crypto/x509/pkix $GOROOT/src/crypto/x509/pkix
//go:generate extract -stdlib database/sql $GOROOT/src/database/sql
//go:generate extract -stdlib database/sql/driver $GOROOT/src/database/sql/driver
//go:generate extract -stdlib debug/dwarf $GOROOT/src/debug/dwarf
//go:generate extract -stdlib debug/elf $GOROOT/src/debug/elf
//go:generate extract -stdlib debug/gosym $GOROOT/src/debug/gosym
//go:generate extract -stdlib debug/macho $GOROOT/src/debug/macho
//go:generate extract -stdlib debug/buildinfo $GOROOT/src/debug/buildinfo
//go:generate extract -stdlib debug/pe $GOROOT/src/debug/pe
//go:generate extract -stdlib debug/plan9obj $GOROOT/src/debug/plan9obj
//go:generate extract -stdlib embed $GOROOT/src/embed
//go:generate extract -stdlib encoding $GOROOT/src/encoding
//go:generate extract -stdlib encoding/ascii85 $GOROOT/src/encoding/ascii85
//go:generate extract -stdlib encoding/asn1 $GOROOT/src/encoding/asn1
//go:generate extract -stdlib encoding/base32 $GOROOT/src/encoding/base32
//go:generate extract -stdlib encoding/base64 $GOROOT/src/encoding/base64
//go:generate extract -stdlib encoding/binary $GOROOT/src/encoding/binary
//go:generate extract -stdlib encoding/csv $GOROOT/src/encoding/csv
//go:generate extract -stdlib encoding/gob $GOROOT/src/encoding/gob
//go:generate extract -stdlib encoding/hex $GOROOT/src/encoding/hex
//go:generate extract -stdlib encoding/json $GOROOT/src/encoding/json
//go:generate extract -stdlib encoding/pem $GOROOT/src/encoding/pem
//go:generate extract -stdlib encoding/xml $GOROOT/src/encoding/xml
//go:generate extract -stdlib errors $GOROOT/src/errors
//go:generate extract -stdlib expvar $GOROOT/src/expvar
//go:generate extract -stdlib flag $GOROOT/src/flag
//go:generate extract -stdlib fmt $GOROOT/src/fmt
//go:generate extract -stdlib go/ast $GOROOT/src/go/ast
//go:generate extract -stdlib go/build $GOROOT/src/go/build
//go:generate extract -stdlib go/build/constraint $GOROOT/src/go/build/constraint
//go:generate extract -stdlib go/constant $GOROOT/src/go/constant
//go:generate extract -stdlib go/doc $GOROOT/src/go/doc
//go:generate extract -stdlib go/doc/comment $GOROOT/src/go/doc/comment
//go:generate extract -stdlib go/format $GOROOT/src/go/format
//go:generate extract -stdlib go/parser $GOROOT/src/go/parser
//go:generate extract -stdlib go/printer $GOROOT/src/go/printer
//go:generate extract -stdlib go/scanner $GOROOT/src/go/scanner
//go:generate extract -stdlib go/token $GOROOT/src/go/token
//go:generate extract -stdlib go/types $GOROOT/src/go/types
//go:generate extract -stdlib go/importer $GOROOT/src/go/importer
//go:generate extract -stdlib go/version $GOROOT/src/go/version
//go:generate extract -stdlib hash $GOROOT/src/hash
//go:generate extract -stdlib hash/adler32 $GOROOT/src/hash/adler32
//go:generate extract -stdlib hash/crc32 $GOROOT/src/hash/crc32
//go:generate extract -stdlib hash/crc64 $GOROOT/src/hash/crc64
//go:generate extract -stdlib hash/fnv $GOROOT/src/hash/fnv
//go:generate extract -stdlib hash/maphash $GOROOT/src/hash/maphash
//go:generate extract -stdlib html $GOROOT/src/html
//go:generate extract -stdlib html/template $GOROOT/src/html/template
//go:generate extract -stdlib image $GOROOT/src/image
//go:generate extract -stdlib image/color $GOROOT/src/image/color
//go:generate extract -stdlib image/color/palette $GOROOT/src/image/color/palette
//go:generate extract -stdlib image/draw $GOROOT/src/image/draw
//go:generate extract -stdlib image/gif $GOROOT/src/image/gif
//go:generate extract -stdlib image/jpeg $GOROOT/src/image/jpeg
//go:generate extract -stdlib image/png $GOROOT/src/image/png
//go:generate extract -stdlib index/suffixarray $GOROOT/src/index/suffixarray
//go:generate extract -stdlib io $GOROOT/src/io
//go:generate extract -stdlib io/fs $GOROOT/src/io/fs
//go:generate extract -stdlib io/ioutil $GOROOT/src/io/ioutil
//go:generate extract -stdlib log $GOROOT/src/log
//go:generate extract -stdlib log/slog $GOROOT/src/log/slog
//go:generate extract -stdlib log/syslog $GOROOT/src/log/syslog
//go:generate extract -stdlib math $GOROOT/src/math
//go:generate extract -stdlib math/big $GOROOT/src/math/big
//go:generate extract -stdlib math/bits $GOROOT/src/math/bits
//go:generate extract -stdlib math/cmplx $GOROOT/src/math/cmplx
//go:generate extract -stdlib math/rand $GOROOT/src/math/rand
//go:generate extract -stdlib math/rand/v2 $GOROOT/src/math/rand/v2
//go:generate extract -stdlib mime $GOROOT/src/mime
//go:generate extract -stdlib mime/multipart $GOROOT/src/mime/multipart
//go:generate extract -stdlib mime/quotedprintable $GOROOT/src/mime/quotedprintable
//go:generate extract -stdlib net $GOROOT/src/net
//go:generate extract -stdlib net/http $GOROOT/src/net/http
//go:generate extract -stdlib net/http/cgi $GOROOT/src/net/http/cgi
//go:generate extract -stdlib net/http/cookiejar $GOROOT/src/net/http/cookiejar
//go:generate extract -stdlib net/http/fcgi $GOROOT/src/net/http/fcgi
//go:generate extract -stdlib net/http/httptest $GOROOT/src/net/http/httptest
//go:generate extract -stdlib net/http/httptrace $GOROOT/src/net/http/httptrace
//go:generate extract -stdlib net/http/httputil $GOROOT/src/net/http/httputil
//go:generate extract -stdlib net/http/pprof $GOROOT/src/net/http/pprof
//go:generate extract -stdlib net/mail $GOROOT/src/net/mail
//go:generate extract -stdlib net/netip $GOROOT/src/net/netip
//go:generate extract -stdlib net/rpc $GOROOT/src/net/rpc
//go:generate extract -stdlib net/rpc/jsonrpc $GOROOT/src/net/rpc/jsonrpc
//go:generate extract -stdlib net/smtp $GOROOT/src/net/smtp
//go:generate extract -stdlib net/textproto $GOROOT/src/net/textproto
//go:generate extract -stdlib net/url $GOROOT/src/net/url
//go:generate extract -stdlib os $GOROOT/src/os
//go:generate extract -stdlib os/exec $GOROOT/src/os/exec
//go:generate extract -stdlib os/signal $GOROOT/src/os/signal
//go:generate extract -stdlib os/user $GOROOT/src/os/user
//go:generate extract -stdlib path $GOROOT/src/path
//go:generate extract -stdlib path/filepath $GOROOT/src/path/filepath
//go:generate extract -stdlib reflect $GOROOT/src/reflect
//go:generate extract -stdlib regexp $GOROOT/src/regexp
//go:generate extract -stdlib regexp/syntax $GOROOT/src/regexp/syntax
//go:generate extract -stdlib runtime $GOROOT/src/runtime
//go:generate extract -stdlib runtime/cgo $GOROOT/src/runtime/cgo
//go:generate extract -stdlib runtime/coverage $GOROOT/src/runtime/coverage
//go:generate extract -stdlib runtime/debug $GOROOT/src/runtime/debug
//go:generate extract -stdlib runtime/metrics $GOROOT/src/runtime/metrics
//go:generate extract -stdlib runtime/pprof $GOROOT/src/runtime/pprof
//go:generate extract -stdlib runtime/trace $GOROOT/src/runtime/trace
//go:generate extract -stdlib sort $GOROOT/src/sort
//go:generate extract -stdlib strconv $GOROOT/src/strconv
//go:generate extract -stdlib strings $GOROOT/src/strings
//go:generate extract -stdlib structs $GOROOT/src/structs
//go:generate extract -stdlib sync $GOROOT/src/sync
//go:generate extract -stdlib sync/atomic $GOROOT/src/sync/atomic
//go:generate extract -stdlib testing $GOROOT/src/testing
//go:generate extract -stdlib testing/cryptotest $GOROOT/src/testing/cryptotest
//go:generate extract -stdlib testing/fstest $GOROOT/src/testing/fstest
//go:generate extract -stdlib testing/iotest $GOROOT/src/testing/iotest
//go:generate extract -stdlib testing/quick $GOROOT/src/testing/quick
//go:generate extract -stdlib testing/slogtest $GOROOT/src/testing/slogtest
//go:generate extract -stdlib testing/synctest $GOROOT/src/testing/synctest
//go:generate extract -stdlib text/scanner $GOROOT/src/text/scanner
//go:generate extract -stdlib text/tabwriter $GOROOT/src/text/tabwriter
//go:generate extract -stdlib text/template $GOROOT/src/text/template
//go:generate extract -stdlib text/template/parse $GOROOT/src/text/template/parse
//go:generate extract -stdlib time $GOROOT/src/time
//go:generate extract -stdlib unicode $GOROOT/src/unicode
//go:generate extract -stdlib unicode/utf16 $GOROOT/src/unicode/utf16
//go:generate extract -stdlib unicode/utf8 $GOROOT/src/unicode/utf8
//go:generate extract -stdlib unique $GOROOT/src/unique
//go:generate extract -stdlib weak $GOROOT/src/weak
