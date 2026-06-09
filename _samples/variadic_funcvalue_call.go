package main

import "fmt"

// Calling a variadic func VALUE (not a declared func) must pack the trailing
// args into the variadic slice, just as a declared variadic call does.

type wrapper struct{ pre string }

func (w *wrapper) sprintFunc() func(a ...interface{}) string {
	return func(a ...interface{}) string {
		return w.pre + fmt.Sprint(a...)
	}
}

func main() {
	w := &wrapper{pre: ">"}
	fmt.Println(w.sprintFunc()("a", "b", 3)) // immediate call
	f := w.sprintFunc()
	fmt.Println(f("x", 9)) // assigned form
	fmt.Println(w.sprintFunc()())
}

// Output:
// >ab3
// >x9
// >
