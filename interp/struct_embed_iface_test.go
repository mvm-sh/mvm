package interp_test

import (
	"fmt"
	"testing"
)

func TestStructEmbedIfaceNativeBoundary(t *testing.T) {
	const src = `func() int {
	rec := httptest.NewRecorder()
	var w http.ResponseWriter = rec
	type onlyCloseNotifier interface{ http.ResponseWriter }
	s := struct{ onlyCloseNotifier }{w.(onlyCloseNotifier)}
	http.Error(s, "boom", http.StatusInternalServerError)
	return rec.Code
}()`
	i := newAutoImportInterp(t)
	r, err := i.Eval("test", src)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "500" {
		t.Errorf("rec.Code = %q, want 500", got)
	}
}
