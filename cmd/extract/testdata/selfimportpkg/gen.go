//go:build ignore

// This generator is excluded from the build but, like time/genzabbrs.go,
// imports the package it lives in. The filename-only scanner harvested its
// self-import too.
package main

import (
	_ "selfimportpkg"
)

func main() {}
