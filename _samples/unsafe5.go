package main

import "unsafe"

type layout struct {
	flag byte
	_    [7]byte
	off  int64
	name string
}

const (
	OffFlag = unsafe.Offsetof(layout{}.flag)
	OffOff  = unsafe.Offsetof(layout{}.off)
	OffName = unsafe.Offsetof(layout{}.name)
)

func main() {
	println(OffFlag, OffOff, OffName)
}

// Output:
// 0 8 16
