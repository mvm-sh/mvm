package stubs

import (
	"github.com/mvm-sh/mvm/runtype"
)

// FillMethods installs methods into a reserved rtype in place (the reserve/fill
// path), resolving each method's stub slot first.
func FillMethods(res *runtype.Reservation, methods []Method) error {
	stubs, err := acquireSlots(methods)
	if err != nil {
		return err
	}
	specs := make([]runtype.MethodSpec, len(methods))
	for i, m := range methods {
		specs[i] = runtype.MethodSpec{
			Name:     m.Name,
			Exported: m.Exported,
			Sig:      m.Sig,
			StubPC:   stubs[i],
		}
	}
	return res.Fill(specs)
}

// acquireSlots claims one stub-pool slot per method, returning the slot PCs.
// On mid-batch failure it releases the handlers already claimed (freeing their
// closure captures); the slot indices stay consumed, as counters are monotonic.
func acquireSlots(methods []Method) ([]uintptr, error) {
	stubs := make([]uintptr, len(methods))
	releases := make([]func(), 0, len(methods))
	for i, m := range methods {
		pc, release, err := acquireSlot(m)
		if err != nil {
			for _, r := range releases {
				r()
			}
			return nil, err
		}
		stubs[i] = pc
		releases = append(releases, release)
	}
	return stubs, nil
}
