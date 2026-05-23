// Package stdlib provides wrappers of standard library packages to be imported natively in mvm.
package stdlib

import "reflect"

// Values variable stores the map of stdlib values per package.
var Values = map[string]map[string]reflect.Value{}

// ConstValues stores, per package, the exact (arbitrary-precision) textual form
// of bridged floating-point constants whose reflect.Value bridge would lose
// precision (e.g. math.Pi). It lets the compiler fold constant expressions such
// as 100000*math.Pi at full precision and round once, matching the Go compiler.
// The string is go/constant.Value.ExactString() (a decimal or "num/den"
// rational), reconstructed via goparser.ConstFromExact at import time.
var ConstValues = map[string]map[string]string{}
