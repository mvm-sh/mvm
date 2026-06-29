package runtype

import "sync/atomic"

// nextSyntheticHash returns a fresh Hash for a synthesized rtype.
// Hash feeds the itab cache buckets; collisions only slow lookups, but
// uniqueness avoids them.
// Golden-ratio stride gives good distribution at zero coordination.
// Initial seed is non-zero because Hash=0 is reserved by some runtime paths.
func nextSyntheticHash() uint32 {
	return synthHashCounter.Add(synthHashStride)
}

const synthHashStride uint32 = 0x9E3779B9 // golden ratio fraction

var synthHashCounter atomic.Uint32

func init() {
	synthHashCounter.Store(synthHashStride)
}
