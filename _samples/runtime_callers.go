package main

import (
	"fmt"
	"runtime"
	"strings"
)

func inner() {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(0, pcs)
	sawGoexit := false
	shown := 0
	for i := 0; i < n; i++ {
		fn := runtime.FuncForPC(pcs[i])
		if fn == nil {
			continue
		}
		if fn.Name() == "runtime.goexit" {
			sawGoexit = true
			continue
		}
		file, line := fn.FileLine(pcs[i])
		if !strings.HasPrefix(file, "modfs/") {
			// Host tail (testing.tRunner etc.): file/line vary by environment.
			continue
		}
		fmt.Printf("frame %d: %s @ %s:%d\n", shown, fn.Name(), file, line)
		shown++
	}
	fmt.Printf("interpreted frames: %d, reached goexit: %v\n", shown, sawGoexit)
}

func middle() {
	inner()
}

func main() {
	middle()
}

// Output:
// frame 0: _samples.inner @ modfs/_samples/runtime_callers.go:11
// frame 1: _samples.middle @ modfs/_samples/runtime_callers.go:35
// frame 2: _samples.main @ modfs/_samples/runtime_callers.go:39
// interpreted frames: 3, reached goexit: true
