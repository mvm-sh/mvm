package goparser

import "path"

// PackageName returns the identifier used to reference the package given its
// import path: the last segment, or the second-to-last when the last matches
// "v[0-9]*" (module versioning suffix).
func PackageName(importPath string) string {
	d, f := path.Split(importPath)
	if ok, _ := path.Match(f, "v[0-9]*"); d != "" && ok {
		return path.Base(d)
	}
	return f
}
