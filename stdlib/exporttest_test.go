package stdlib

import (
	"reflect"
	"testing"
)

// TestStringFinderTables checks the ported Boyer-Moore tables against the
// expected values from $GOROOT/src/strings/search_test.go (TestFinderCreation),
// so DumpTables stays faithful to the upstream algorithm.
func TestStringFinderTables(t *testing.T) {
	cases := []struct {
		pattern string
		bad     map[byte]int
		good    []int
	}{
		{"abc", map[byte]int{'a': 2, 'b': 1, 'c': 3}, []int{5, 4, 1}},
		{"mississi", map[byte]int{'i': 3, 'm': 7, 's': 1}, []int{15, 14, 13, 7, 11, 10, 7, 1}},
		{"abcxxxabc", map[byte]int{'a': 2, 'b': 1, 'c': 6, 'x': 3}, []int{14, 13, 12, 11, 10, 9, 11, 10, 1}},
	}
	for _, c := range cases {
		f := makeStringFinder(c.pattern)
		for b := 0; b < 256; b++ {
			want := c.bad[byte(b)]
			if want == 0 {
				want = len(c.pattern)
			}
			if f.badCharSkip[b] != want {
				t.Errorf("%q badCharSkip[%q] = %d, want %d", c.pattern, byte(b), f.badCharSkip[b], want)
			}
		}
		if !reflect.DeepEqual(f.goodSuffixSkip, c.good) {
			t.Errorf("%q goodSuffixSkip = %v, want %v", c.pattern, f.goodSuffixSkip, c.good)
		}
	}
}

// TestExportTestOverlay verifies the overlay exposes the export_test stand-ins
// (merging over the real bridge base is covered end-to-end by `mvm test
// strings`, where strings.Index and StringFind both resolve). The base symbols
// aren't asserted here because this package's own test doesn't load the core
// bindings (importing them would cycle).
func TestExportTestOverlay(t *testing.T) {
	strs := TestOverlay()["strings"]
	for _, name := range []string{"StringFind", "DumpTables"} {
		if _, ok := strs[name]; !ok {
			t.Errorf("overlay missing export_test stand-in %q", name)
		}
	}
	// StringFind(pattern, text) == Index(text, pattern).
	sf := TestValues["strings"]["StringFind"]
	got := sf.Call([]reflect.Value{reflect.ValueOf("nan"), reflect.ValueOf("banana")})[0].Int()
	if got != 2 {
		t.Errorf("StringFind(nan, banana) = %d, want 2", got)
	}
	ibp := TestValues["bytes"]["IndexBytePortable"]
	pos := ibp.Call([]reflect.Value{reflect.ValueOf([]byte("banana")), reflect.ValueOf(byte('n'))})[0].Int()
	if pos != 2 {
		t.Errorf("IndexBytePortable(banana, n) = %d, want 2", pos)
	}
}
