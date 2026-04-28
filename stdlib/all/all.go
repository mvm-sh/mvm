// Package all is the convenience aggregator for stdlib bindings: blank-import
// it to get the full set (core + ext + jsonx). Consumers that want a smaller
// link footprint should import stdlib/core directly and skip this package.
package all

import (
	_ "github.com/mvm-sh/mvm/stdlib/core"
	_ "github.com/mvm-sh/mvm/stdlib/ext"
	_ "github.com/mvm-sh/mvm/stdlib/jsonx"
)
