package stubs

import "sort"

// PoolStat reports one stub pool's monotonic high-water mark against its
// capacity. Used is the number of slots consumed this process (pools never
// reclaim), Cap the generated slot count.
type PoolStat struct {
	Name string
	Used uint32
	Cap  uint32
}

// typedPools lists each typed shape's high-water accessor and capacity, so
// HighWater can report them alongside the word pools.
var typedPools = []struct {
	name string
	used func() uint32
	cap  uint32
}{
	{"S1", SlotsUsedS1, poolSizeS1},
	{"S2", SlotsUsedS2, poolSizeS2},
	{"S3", SlotsUsedS3, poolSizeS3},
	{"S4", SlotsUsedS4, poolSizeS4},
	{"S5", SlotsUsedS5, poolSizeS5},
	{"S6", SlotsUsedS6, poolSizeS6},
	{"S7", SlotsUsedS7, poolSizeS7},
	{"S8", SlotsUsedS8, poolSizeS8},
	{"S9", SlotsUsedS9, poolSizeS9},
	{"S10", SlotsUsedS10, poolSizeS10},
	{"S11", SlotsUsedS11, poolSizeS11},
	{"S12", SlotsUsedS12, poolSizeS12},
	{"S13", SlotsUsedS13, poolSizeS13},
	{"S14", SlotsUsedS14, poolSizeS14},
	{"S15", SlotsUsedS15, poolSizeS15},
	{"S16", SlotsUsedS16, poolSizeS16},
	{"S17", SlotsUsedS17, poolSizeS17},
	{"S18", SlotsUsedS18, poolSizeS18},
	{"S19", SlotsUsedS19, poolSizeS19},
	{"S20", SlotsUsedS20, poolSizeS20},
	{"S21", SlotsUsedS21, poolSizeS21},
	{"S22", SlotsUsedS22, poolSizeS22},
	{"S23", SlotsUsedS23, poolSizeS23},
	{"S24", SlotsUsedS24, poolSizeS24},
	{"S25", SlotsUsedS25, poolSizeS25},
	{"S26", SlotsUsedS26, poolSizeS26},
	{"S27", SlotsUsedS27, poolSizeS27},
	{"S28", SlotsUsedS28, poolSizeS28},
	{"S29", SlotsUsedS29, poolSizeS29},
	{"S30", SlotsUsedS30, poolSizeS30},
	{"S31", SlotsUsedS31, poolSizeS31},
	{"S32", SlotsUsedS32, poolSizeS32},
	{"S33", SlotsUsedS33, poolSizeS33},
	{"S34", SlotsUsedS34, poolSizeS34},
	{"S35", SlotsUsedS35, poolSizeS35},
	{"S36", SlotsUsedS36, poolSizeS36},
	{"S37", SlotsUsedS37, poolSizeS37},
	{"S38", SlotsUsedS38, poolSizeS38},
}

// HighWater returns the high-water mark and capacity of every stub pool (typed
// shapes plus word shapes), sorted by name. Used to right-size pools: a pool
// whose Used stays far below Cap across the heaviest single-process workloads
// can shrink (each slot is one generated function, see docs/modules/stubs.md).
func HighWater() []PoolStat {
	out := make([]PoolStat, 0, len(typedPools)+8)
	for _, t := range typedPools {
		out = append(out, PoolStat{Name: t.name, Used: t.used(), Cap: t.cap})
	}
	wordPools.Range(func(k, v any) bool {
		p := v.(*wordPool)
		out = append(out, PoolStat{Name: "W_" + k.(string), Used: p.next.Load(), Cap: p.cap})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
