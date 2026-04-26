package scan_test

import (
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/scan"
)

var benchScanner = scan.NewScanner(golang.GoSpec)

// BenchmarkScanIdent measures scanning of identifiers and operators (no strings/blocks).
func BenchmarkScanIdent(b *testing.B) {
	src := strings.Repeat("abc + def - ghi * jkl / mno\n", 100)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for range b.N {
		_, _ = benchScanner.Scan(src, false)
	}
}

// BenchmarkScanString measures scanning of string literals.
func BenchmarkScanString(b *testing.B) {
	src := strings.Repeat(`x = "hello world" + "foo bar baz"`+"\n", 100)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for range b.N {
		_, _ = benchScanner.Scan(src, false)
	}
}

// BenchmarkScanBlock measures scanning of nested blocks (exercises getBlock + regex).
func BenchmarkScanBlock(b *testing.B) {
	src := strings.Repeat(`f(a, b, "str", g(x, y))`+"\n", 100)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for range b.N {
		_, _ = benchScanner.Scan(src, false)
	}
}

// BenchmarkScanFunc measures scanning of a realistic function body.
func BenchmarkScanFunc(b *testing.B) {
	src := `func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

func main() {
	for i := 0; i < 30; i++ {
		fmt.Println("fib", i, "=", fib(i))
	}
}
`
	src = strings.Repeat(src, 20)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for range b.N {
		_, _ = benchScanner.Scan(src, false)
	}
}

// BenchmarkScanDeepBlock measures scanning of deeply nested blocks.
func BenchmarkScanDeepBlock(b *testing.B) {
	inner := `f(g(h("abc", x+y), z), w)`
	src := strings.Repeat(inner+"\n", 100)
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for range b.N {
		_, _ = benchScanner.Scan(src, false)
	}
}
