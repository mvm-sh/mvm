package goparser

import "sync"

// Overlays are extra .go files appended to a source-loaded package, supplying
// pure-Go bodies for assembly-only functions (e.g. x/sys/unix.RawSyscall).
var (
	overlayMu sync.Mutex
	overlays  = map[string][]PackageSource{}
)

// RegisterSourceOverlay queues a source file to append to importPath. Safe from init.
func RegisterSourceOverlay(importPath, name, content string) {
	overlayMu.Lock()
	defer overlayMu.Unlock()
	overlays[importPath] = append(overlays[importPath], PackageSource{Name: name, Content: content})
}

func sourceOverlay(importPath string) []PackageSource {
	if importPath == "" {
		return nil
	}
	overlayMu.Lock()
	defer overlayMu.Unlock()
	return overlays[importPath]
}
