package main

import (
	"fmt"
	"reflect"
	"sort"
)

// func(*T) methods -- word shape "p_", as in grpctest.RunSubTests' func(*testing.T)
// subtest methods. Without a stub pool for it they drop from the synth rtype, so
// reflect.TypeOf(x).NumMethod() saw zero.
type suite struct{}

func (suite) TestAlpha(x *int) {}
func (suite) TestBeta(x *int)  {}

func main() {
	rt := reflect.TypeOf(suite{})
	fmt.Println("NumMethod:", rt.NumMethod())
	names := make([]string, rt.NumMethod())
	for i := range names {
		names[i] = rt.Method(i).Name
	}
	sort.Strings(names)
	fmt.Println(names)
}

// Output:
// NumMethod: 2
// [TestAlpha TestBeta]
