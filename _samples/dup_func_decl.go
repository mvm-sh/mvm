package main

// Known bug (pre-existing): two top-level functions with the same name make the
// interpreter HANG (busy infinite loop at ~100% CPU) instead of reporting a
// "foo redeclared" compile error the way gc does. Reproduces with any duplicate
// top-level func name (main included), single-file or across files. Surfaced by
// the issue #19 multi-file `mvm run` work: `mvm run a.go b.go` where two files
// each declare `func main` now reaches this hang (previously only the first file
// was compiled). Root cause is the duplicate-symbol handling, not the multi-file
// change. Needs a redeclaration check at func registration (goparser parseFuncDecl
// / SymAdd) so the duplicate errors instead of looping.
//
// WARNING: do NOT remove the skip directive below without fixing the bug first --
// this program does not terminate, so an un-skipped run would hang the suite.

func foo() {}
func foo() {}

func main() { foo() }

// skip: HANG (infinite loop) on duplicate top-level func decl; un-skip only after a redeclaration check is added
