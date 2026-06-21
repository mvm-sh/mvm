package interp

import "testing"

// A pointer-receiver method (Push) is promoted from an embedded value field
// (Heap) and reached through the native container/heap bridge. The receiver
// must be a pointer to the real embedded field so append propagates back into
// the owning struct; makeMethodCell used to box a copy, so heap.Push silently
// dropped every element. Shape mirrors gonum kdtree's DistKeeper{Heap}.
func TestPromotedPtrRecvHeapWriteback(t *testing.T) {
	src := `package main

import (
	"container/heap"
	"fmt"
)

type Item struct{ Dist float64 }
type Heap []Item

func (h *Heap) Len() int             { return len(*h) }
func (h *Heap) Less(i, j int) bool   { return (*h)[i].Dist > (*h)[j].Dist }
func (h *Heap) Swap(i, j int)        { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }
func (h *Heap) Push(x interface{})   { (*h) = append(*h, x.(Item)) }
func (h *Heap) Pop() (i interface{}) { i, *h = (*h)[len(*h)-1], (*h)[:len(*h)-1]; return i }

type DistKeeper struct {
	Heap
}

func (k *DistKeeper) Keep(c Item) {
	if c.Dist <= k.Heap[0].Dist {
		heap.Push(k, c)
	}
}

func main() {
	k := &DistKeeper{Heap{{Dist: 100}}}
	k.Keep(Item{Dist: 5})
	k.Keep(Item{Dist: 8})
	k.Keep(Item{Dist: 3})
	fmt.Println(len(k.Heap))
}
`
	if got := runMain(t, "promoheap", src); got != "4\n" {
		t.Errorf("got %q, want %q", got, "4\n")
	}
}
