package main

// General compile-time constant folding: each expression below has only
// constant operands and is folded to a single value at compile time (verified
// by disassembly elsewhere); here we just check the results match Go.

func main() {
	println(1 + 2*3) // precedence: 1 + 6
	println((1 + 2) * 3)
	println(7 % 3)
	println(7 / 2) // integer division
	println(-1 * 5)
	println(^0)    // bitwise complement of untyped 0 is -1
	println(!true) // unary not on a constant
	println(1<<3 | 2)
	println(100 * 100)
}

// Output:
// 7
// 9
// 1
// 3
// -5
// -1
// false
// 10
// 10000
