package interptest

import "testing"

// net/http is interpreted from the mirror on wasm (the native bridge is dropped
// there); it pulls golang.org/x/net + x/text from the embedded vendor subset, and
// reaches bufio/net/textproto/net/url, all interpreted. These TestSynth* cases run
// under the wasm CI so the whole HTTP/1.1 request/response path stays byte-identical.
// On native net/http stays bridged, so the same tests exercise the bridge.

func TestSynthNetHTTPRequest(t *testing.T) {
	const src = `package main
import ("bufio"; "fmt"; "io"; "net/http"; "strings")
func main() {
	raw := "POST /submit?x=1 HTTP/1.1\r\nHost: example.com\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nhello"
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil { panic(err) }
	body, _ := io.ReadAll(req.Body)
	fmt.Printf("%s %s %s %q %q %s\n", req.Method, req.URL.Path, req.Host,
		req.Header.Get("Content-Type"), body, req.URL.Query().Get("x"))
}`
	want := "POST /submit example.com \"text/plain\" \"hello\" 1\n"
	if got := evalOut(t, "nethttp_req.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSynthNetHTTPServeMux(t *testing.T) {
	const src = `package main
import ("fmt"; "io"; "net/http"; "strings")
type rw struct { h http.Header; b strings.Builder; code int }
func (w *rw) Header() http.Header { return w.h }
func (w *rw) Write(b []byte) (int, error) { return w.b.Write(b) }
func (w *rw) WriteHeader(c int) { w.code = c }
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/hi/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Tag", "ok")
		w.WriteHeader(201)
		fmt.Fprintf(w, "hi %s", strings.TrimPrefix(r.URL.Path, "/hi/"))
	})
	w := &rw{h: http.Header{}}
	r, _ := http.NewRequest("GET", "/hi/there", nil)
	mux.ServeHTTP(w, r)
	fmt.Printf("%d %s %q\n", w.code, w.h.Get("X-Tag"), w.b.String())
	_ = io.Discard
}`
	want := "201 ok \"hi there\"\n"
	if got := evalOut(t, "nethttp_mux.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
