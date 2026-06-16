package main

import "fmt"

// Taking the address of an error-typed (or any native-interface) struct field
// yields *interface{} (mvm stores interfaces as eface); a *error field of the
// same struct must therefore also be *interface{} or the composite literal's
// reflect.Set panics "*interface{} is not assignable to *error". Modeled on
// x/net/http2's stickyErrWriter{err: &cc.werr}.
type sticky struct {
	err *error
}

type conn struct {
	werr error
}

func main() {
	cc := &conn{}
	s := sticky{err: &cc.werr}
	*s.err = fmt.Errorf("boom")
	fmt.Println(cc.werr)
}

// Output:
// boom
