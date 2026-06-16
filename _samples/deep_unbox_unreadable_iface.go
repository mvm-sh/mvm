package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
)

// Passing a native struct with a populated unexported interface field (here a
// *log.Logger whose out is io.Discard, a zero-size concrete) as an `any` arg to
// a native func makes the deep-unbox walk see an interface that is non-nil yet
// reflects a zero Elem; it must skip it, not panic. Modeled on http2 storing
// its *http.Server (with ErrorLog set) into a context value.
func main() {
	h := &http.Server{}
	h.ErrorLog = log.New(io.Discard, "", 0)
	ctx := context.WithValue(context.Background(), http.ServerContextKey, h)
	fmt.Println("stored:", ctx.Value(http.ServerContextKey) != nil)
}

// Output:
// stored: true
