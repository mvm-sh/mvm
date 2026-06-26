package interptest

import "testing"

// container/heap, container/list, container/ring are interpreted from the mirror
// on wasm (native bridges dropped there). heap is the dispatcher-trap case: an
// interpreted type implements heap.Interface, and heap.Init/Push/Pop call its
// Less/Swap/Push/Pop. On wasm a native heap bridge dispatching those interpreted
// methods would hit the shared-PC trap, so heap must be interpreted. These
// TestSynth* cases run under the wasm CI and guard that the whole family stays
// byte-identical (native = bridge path, wasm = mirror path).

func TestSynthContainerHeap(t *testing.T) {
	const src = `package main
import ("container/heap"; "fmt")
type IntHeap []int
func (h IntHeap) Len() int           { return len(h) }
func (h IntHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h IntHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *IntHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *IntHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
func main() {
	h := &IntHeap{5, 2, 8, 1, 9, 3}
	heap.Init(h)
	heap.Push(h, 4)
	var out []int
	for h.Len() > 0 {
		out = append(out, heap.Pop(h).(int))
	}
	fmt.Println(out)
}`
	if got, want := evalOut(t, "heap.go", src), "[1 2 3 4 5 8 9]\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSynthContainerList(t *testing.T) {
	const src = `package main
import ("container/list"; "fmt")
func main() {
	l := list.New()
	l.PushBack(1)
	l.PushBack(2)
	l.PushFront(0)
	for e := l.Front(); e != nil; e = e.Next() {
		fmt.Print(e.Value, " ")
	}
	fmt.Println("len", l.Len())
}`
	if got, want := evalOut(t, "list.go", src), "0 1 2 len 3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSynthContainerRing(t *testing.T) {
	const src = `package main
import ("container/ring"; "fmt")
func main() {
	r := ring.New(5)
	for i := 0; i < r.Len(); i++ {
		r.Value = i
		r = r.Next()
	}
	sum := 0
	r.Do(func(v any) { sum += v.(int) }) // native ring.Do calls an interpreted closure
	fmt.Println("sum", sum)
}`
	if got, want := evalOut(t, "ring.go", src), "sum 10\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
