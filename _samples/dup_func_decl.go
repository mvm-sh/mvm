package main

// Two top-level functions with the same name are a redeclaration (like gc's
// "foo redeclared in this block"). mvm rejects it at compile time. Regression
// guard: this used to compile a duplicate function label whose colliding jump
// target hung the VM in an infinite loop. Same check covers methods and a
// duplicate func split across files (mvm run a.go b.go).

func foo() {}
func foo() {}

func main() { foo() }

// Error:
// foo redeclared in this block
