package main

import (
	"fmt"
	"image/color"
	"sort"
)

// Interpreted types satisfying native interfaces drive the synth word-class
// stub dispatch (vm/synth_word.go). On wasm this takes the ABI0 stack-slot
// path (vm/synth_word_abi0.go); the packed sub-word case below (4xuint32 in
// 2 slots) is the one that differs from the register ABI, so it is the key
// end-to-end check that the whole interpreter pipeline runs on wasm.

type label struct{ s string }

func (l label) String() string { return "label(" + l.s + ")" }

type rgb struct{ r, g, b uint8 }

func (c rgb) RGBA() (r, g, b, a uint32) {
	return uint32(c.r) * 0x101, uint32(c.g) * 0x101, uint32(c.b) * 0x101, 0xffff
}

type byVal []int

func (v byVal) Len() int           { return len(v) }
func (v byVal) Less(i, j int) bool { return v[i] < v[j] }
func (v byVal) Swap(i, j int)      { v[i], v[j] = v[j], v[i] }

func main() {
	var s fmt.Stringer = label{"x"}
	fmt.Println(s.String())

	var c color.Color = rgb{0x10, 0x20, 0x30}
	r, g, b, a := c.RGBA()
	fmt.Printf("%#04x %#04x %#04x %#04x\n", r, g, b, a)
	conv := color.RGBAModel.Convert(c).(color.RGBA)
	fmt.Printf("R=%d G=%d B=%d A=%d\n", conv.R, conv.G, conv.B, conv.A)

	d := byVal{5, 3, 8, 1, 9, 2}
	sort.Sort(d)
	fmt.Println(d)
}

// Output:
// label(x)
// 0x1010 0x2020 0x3030 0xffff
// R=16 G=32 B=48 A=255
// [1 2 3 5 8 9]
