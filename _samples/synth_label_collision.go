package main

// Variables named like synthetic jump labels (break "for0e", continue
// "for0b", switch end/drop, if/else, short-circuit "x" labels) must not
// collide with them.

func main() {
	for0e, for0b, switch0e, if0e0, if0x0 := 0, 0, 0, 0, 0
	for i := 0; i < 3; i++ {
		if i == 1 {
			break
		}
		for0e++
	}
	for i := 0; i < 3; i++ {
		if i == 0 {
			continue
		}
		for0b++
	}
	switch for0e {
	case 1:
		switch0e++
	default:
		switch0e--
	}
	if for0b == 2 {
		if0e0++
	} else {
		if0e0--
	}
	if for0e == 1 && for0b == 2 {
		if0x0++
	}
	println(for0e, for0b, switch0e, if0e0, if0x0)
}

// Output:
// 1 2 1 1 1
