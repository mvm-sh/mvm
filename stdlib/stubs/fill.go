//go:build !wasm

package stubs

import (
	"github.com/mvm-sh/mvm/runtype"
)

// FillMethods installs methods into a reserved rtype, claiming a stub slot each.
// The returned release nils this call's slot handlers (which capture *Machine);
// callers tie it to the Machine via Interp.Close. nil only when err != nil.
func FillMethods(res *runtype.Reservation, methods []Method) (release func(), err error) {
	stubs, releases, err := acquireSlots(methods)
	if err != nil {
		return nil, err
	}
	specs := make([]runtype.MethodSpec, len(methods))
	for i, m := range methods {
		specs[i] = runtype.MethodSpec{
			Name:     m.Name,
			Exported: m.Exported,
			PkgPath:  m.PkgPath,
			Sig:      m.Sig,
			StubPC:   stubs[i],
		}
	}
	if err := res.Fill(specs); err != nil {
		runReleases(releases)
		return nil, err
	}
	return func() { runReleases(releases) }, nil
}

func runReleases(releases []func()) {
	for _, r := range releases {
		r()
	}
}

// acquireSlots claims one slot per method, returning slot PCs and release closures.
// On mid-batch failure it releases what it claimed; slot indices stay consumed.
func acquireSlots(methods []Method) (stubs []uintptr, releases []func(), err error) {
	stubs = make([]uintptr, len(methods))
	releases = make([]func(), 0, len(methods))
	for i, m := range methods {
		pc, release, err := acquireSlot(m)
		if err != nil {
			runReleases(releases)
			return nil, nil, err
		}
		stubs[i] = pc
		releases = append(releases, release)
	}
	return stubs, releases, nil
}
