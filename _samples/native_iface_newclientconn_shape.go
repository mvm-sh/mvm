package main

import (
	"fmt"
	"net"
	"net/http"
	"reflect"
)

// An interpreted type satisfies a native interface via a method whose signature
// is func(net.Conn, func()) (http.RoundTripper, error) -- word shape "ppp_pppp".
// This is net/http's unexported newClientConner; without a stub pool for the shape
// the method could not attach to the synth rtype and the native assertion failed.
type connRT struct{}

func (connRT) RoundTrip(req *http.Request) (*http.Response, error) { return nil, nil }

func (connRT) NewClientConn(c net.Conn, hook func()) (http.RoundTripper, error) {
	return nil, nil
}

type newClientConner interface {
	NewClientConn(nc net.Conn, internalStateHook func()) (http.RoundTripper, error)
}

func main() {
	rt := reflect.TypeOf(any(connRT{}))
	ncc := reflect.TypeOf((*newClientConner)(nil)).Elem()
	fmt.Println("implements:", rt.Implements(ncc))
}

// Output:
// implements: true
