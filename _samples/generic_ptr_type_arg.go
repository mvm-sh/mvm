package main

// A generic type instantiated with a pointer type argument, used as a
// self-referential field (mirrors net/http's mapping[string, *routingNode]).
// The monomorphized name "box#_node" must stay a single identifier so the
// pointer-receiver methods register correctly and instantiation terminates.

type node struct {
	name     string
	children box[*node]
}

type box[T any] struct {
	val T
}

func (b *box[T]) set(v T) { b.val = v }
func (b *box[T]) get() T  { return b.val }

func main() {
	root := &node{name: "root"}
	leaf := &node{name: "leaf"}
	root.children.set(leaf)
	println(root.name, root.children.get().name)
}

// Output:
// root leaf
