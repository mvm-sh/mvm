package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"
)

// An interpreted type satisfies native net.Conn (here checked by the native
// tls.Server, which takes a net.Conn) only if every method attaches a synth
// stub, including SetDeadline/SetReadDeadline/SetWriteDeadline(time.Time) error
// -- word-shape iip_pp (time.Time = 3 words, error = 2). Modeled on x/net/http2's
// synctestNetConn.
type fakeConn struct{}

func (fakeConn) Read([]byte) (int, error)         { return 0, nil }
func (fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return nil }
func (fakeConn) RemoteAddr() net.Addr             { return nil }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }

func main() {
	c := tls.Server(fakeConn{}, &tls.Config{})
	fmt.Println("net.Conn satisfied:", c != nil)
}

// Output:
// net.Conn satisfied: true
