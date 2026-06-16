package main

import (
	"fmt"
	"net/http"
)

// An interpreted type satisfies a native interface (http.RoundTripper) via a
// method whose signature is func(*http.Request) (*http.Response, error) -- word
// shape "p_ppp". Without a generated stub pool for that shape the method could
// not attach, so RegisterProtocol's reflect.Call rejected the value.
type rt struct{ tag string }

func (r rt) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, nil
}

func main() {
	t := &http.Transport{}
	t.RegisterProtocol("https", rt{tag: "x"})
	fmt.Println("registered")
}

// Output:
// registered
