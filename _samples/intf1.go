package main

import "fmt"

type T struct{}

func (t *T) String() string { return "TTT" }

func main() {
	t := &T{}
	fmt.Println("t:", t)
}

// Output:
// t: TTT
