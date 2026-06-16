package main

import "fmt"

// An int32-immediate compare-and-branch fused `>imm` into `<imm+1`; when imm was
// MaxInt32 (2147483647) the +1 overflowed the int32 immediate field to MinInt32,
// inverting the test so the branch was wrongly taken.
func gt(sum int64) bool {
	const maxWindow = 1<<31 - 1
	if sum > maxWindow {
		return true
	}
	return false
}

func main() {
	var sum int64 = 1
	const maxWindow = 1<<31 - 1
	if sum > maxWindow {
		fmt.Println("direct: WRONG (branch taken)")
	} else {
		fmt.Println("direct: ok")
	}
	if !(sum > maxWindow) {
		fmt.Println("negated: ok")
	} else {
		fmt.Println("negated: WRONG")
	}
	fmt.Println("func:", gt(1), gt(maxWindow), gt(maxWindow+1))
}

// Output:
// direct: ok
// negated: ok
// func: false false true
