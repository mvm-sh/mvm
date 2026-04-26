// Package stdlib provides wrappers of standard library packages to be imported natively in mvm.
package stdlib

import "reflect"

// Values variable stores the map of stdlib values per package.
var Values = map[string]map[string]reflect.Value{}
