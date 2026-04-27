package main

import (
	"fmt"
	"sort"
)

type byAge []struct {
	name string
	age  int
}

func (b byAge) Len() int           { return len(b) }
func (b byAge) Less(i, j int) bool { return b[i].age < b[j].age }
func (b byAge) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func main() {
	people := byAge{
		{"Bob", 30},
		{"Alice", 25},
		{"Charlie", 35},
	}
	sort.Sort(people)
	for _, p := range people {
		fmt.Println(p.name, p.age)
	}
	fmt.Println("sorted:", sort.IsSorted(people))
}

// Output:
// Alice 25
// Bob 30
// Charlie 35
// sorted: true
