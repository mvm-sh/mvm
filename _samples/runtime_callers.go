package main

import (
	"fmt"
	"runtime"
)

func inner() {
	pcs := make([]uintptr, 8)
	n := runtime.Callers(0, pcs)
	for i := 0; i < n; i++ {
		fn := runtime.FuncForPC(pcs[i])
		if fn == nil {
			fmt.Printf("frame %d: nil func\n", i)
			continue
		}
		file, line := fn.FileLine(pcs[i])
		fmt.Printf("frame %d: %s @ %s:%d\n", i, fn.Name(), file, line)
	}
	fmt.Printf("total frames: %d\n", n)
}

func middle() {
	inner()
}

func main() {
	middle()
}

// skip: file path differs between `mvm run` and interp.TestFile harness
// (the test loads via path, mvm run via import name). The bridge
// virtualization is exercised via pkg/errors tests instead.
