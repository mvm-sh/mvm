package main

type E struct {
	len int
	cap int
	new int
}

func main() {
	e := E{len: 1, cap: 2, new: 3} // notice field name collision with builtin name
	println(e.len, e.cap, e.new)
}

// Output:
// 1 2 3
