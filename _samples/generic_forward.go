package main

import "fmt"

func MapOf[K comparable, V any](m map[K]V) Map[K, V] {
	return Map[K, V]{m}
}

type Map[K comparable, V any] struct {
	m map[K]V
}

func main() {
	mm := MapOf(map[string]int{"a": 1})
	fmt.Println(mm)
}

// Output:
// {map[a:1]}
