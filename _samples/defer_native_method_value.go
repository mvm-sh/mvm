package main

import (
	"bytes"
	"fmt"
)

// defer of a native method value must capture the receiver by value, not alias
// its slot (a later write would corrupt the deferred call).
// Modeled on x/net/http2 server.go `defer settingsTimer.Stop()`.
func run() {
	b := &bytes.Buffer{}
	defer b.WriteByte('x') // bound to the real buffer, not the later nil
	b = nil
	_ = b
}

func main() {
	run()
	fmt.Println("ok")
}

// Output:
// ok
