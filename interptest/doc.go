// Package interptest holds the end-to-end behavior tests for the mvm interpreter.
// They drive the whole pipeline (scan -> goparser -> mtype/runtype -> comp -> vm -> interp)
// through interp.Eval, and serve as the project's integration coverage for those packages.
// Tests requiring interp internals stay in package interp.
package interptest
