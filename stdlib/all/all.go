// Package all is the convenience aggregator for stdlib bindings: blank-import
// it to get the full set (core + ext + jsonx). Consumers that want a smaller
// link footprint should import stdlib/core directly and skip this package.
package all

import (
	_ "github.com/mvm-sh/mvm/stdlib/core"    // init all bindings
	_ "github.com/mvm-sh/mvm/stdlib/errorsx" // init all bindings
	_ "github.com/mvm-sh/mvm/stdlib/ext"     // init all bindings
	_ "github.com/mvm-sh/mvm/stdlib/fmtx"    // init all bindings

	_ "github.com/mvm-sh/mvm/stdlib/gobx"  // init all bindings
	_ "github.com/mvm-sh/mvm/stdlib/jsonx" // init all bindings
	_ "github.com/mvm-sh/mvm/stdlib/xmlx"  // init all bindings
)
