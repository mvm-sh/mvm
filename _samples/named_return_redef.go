package main

import "fmt"

type Tag struct{ a int }

func parseTag() (Tag, int) { return Tag{a: 7}, 42 }

func driver(fn func() (id Tag, skip bool)) {
	id, skip := fn()
	fmt.Println(id, skip)
}

func main() {
	driver(func() (id Tag, skip bool) {
		id, end := parseTag()
		_ = end
		id.a += 1
		return id, false
	})
}

// Output:
// {8} false
