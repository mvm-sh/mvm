package main

// Field access through a nil pointer receiver (x.v) must raise a recoverable nil deref, not an uncatchable reflect panic.

type T struct{ v int }

func (x *T) get() int { return x.v }

func main() {
	defer func() {
		if recover() != nil {
			println("recovered")
		}
	}()
	var x *T
	println(x.get())
}

// Output:
// recovered
