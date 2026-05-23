package main

// Classic Eratosthenes sieve with a named-constant bound. The inner loop
// `for j := i*i; j <= N; j += i` is bounded by the const N, which exercises
// const-identifier inlining (N folded into an immediate comparison) and the
// `sieve[N+1]` size folds at compile time.

const N = 100

func main() {
	sieve := make([]bool, N+1)
	count := 0
	for i := 2; i <= N; i++ {
		if !sieve[i] {
			count++
			for j := i * i; j <= N; j += i {
				sieve[j] = true
			}
		}
	}
	println(count)
}

// Output:
// 25
