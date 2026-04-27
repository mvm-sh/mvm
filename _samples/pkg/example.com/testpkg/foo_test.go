package testpkg

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Errorf("Add(2,3) = %d, want 5", got)
	}
}

func TestSubFails(t *testing.T) {
	if got := Sub(5, 2); got != 99 {
		t.Errorf("Sub(5,2) = %d, want 99", got)
	}
}
