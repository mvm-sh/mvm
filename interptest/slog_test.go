package interptest

import "testing"

// log/slog: mirrored on wasm, bridged on native. slog dispatches handler.Handle
// on a slog.Handler interface; bridged on wasm, slog.New(interpretedHandler)
// failed at the boundary ("reflect: Call using *X as type slog.Handler").
// Mirroring keeps slog interpreted so a custom interpreted handler works.
func TestSynthSlog(t *testing.T) {
	const src = `package main
import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
)
type capHandler struct{ msgs *[]string }
func (h *capHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capHandler) Handle(_ context.Context, r slog.Record) error {
	*h.msgs = append(*h.msgs, r.Message); return nil
}
func (h *capHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capHandler) WithGroup(string) slog.Handler      { return h }
func strip(g []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(g) == 0 { return slog.Attr{} }
	return a
}
func main() {
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{ReplaceAttr: strip}
	slog.New(slog.NewTextHandler(&buf, opts)).Info("hi", "n", 42, "ok", true)
	slog.New(slog.NewJSONHandler(&buf, opts)).Warn("warn", "code", 7)
	fmt.Print(buf.String())
	var got []string
	c := slog.New(&capHandler{msgs: &got})
	c.Info("one"); c.Error("two")
	fmt.Println("captured", len(got), got[0], got[1])
}`
	want := "level=INFO msg=hi n=42 ok=true\n" +
		`{"level":"WARN","msg":"warn","code":7}` + "\n" +
		"captured 2 one two\n"
	if got := evalOut(t, "slog.go", src); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
