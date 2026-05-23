package main

// Named constants participate in folding (K + 1, S * S) and are inlined as
// immediates when used against a variable (the sieve perf path).

const (
	K = 10_000_000
	S = 5
)

func main() {
	x := K + 1
	println(x)
	println(S * S)
	big := K * K // 10^14, still fits int64
	println(big)
	n := 3
	if n <= K { // K inlined as an immediate comparison
		println("in range")
	}
}

// Output:
// 10000001
// 25
// 100000000000000
// in range
