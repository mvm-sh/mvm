package synth

import (
	"os"
	"sync"
)

// Enabled reports whether the synth-rtype path is selected for this process.
func Enabled() bool {
	enabledOnce.Do(func() {
		enabledVal = os.Getenv("MVM_SYNTH") != ""
	})
	return enabledVal
}

var (
	enabledOnce sync.Once
	enabledVal  bool
)
