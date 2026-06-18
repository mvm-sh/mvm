package stdlib

import (
	"io/fs"
	"os"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/vm"
)

// moduleFS serves files under the synthetic "modfs/" root that mvm's
// virtualized runtime.Caller reports for module-sourced frames.
var moduleFS fs.FS

// RegisterModuleFS installs the module filesystem consulted by the os patcher
// for "modfs/"-prefixed paths. Call once at startup; nil leaves os untouched.
func RegisterModuleFS(fsys fs.FS) { moduleFS = fsys }

const modfsPrefix = "modfs/"

func modfsSub(name string) (string, bool) {
	if moduleFS == nil || !strings.HasPrefix(name, modfsPrefix) {
		return "", false
	}
	return strings.TrimPrefix(name, modfsPrefix), true
}

func init() {
	RegisterPackagePatcher("os", patchOsModfs)
}

func patchOsModfs(_ *vm.Machine, values map[string]vm.Value) {
	values["ReadFile"] = vm.FromReflect(reflect.ValueOf(func(name string) ([]byte, error) {
		if sub, ok := modfsSub(name); ok {
			return fs.ReadFile(moduleFS, sub)
		}
		return os.ReadFile(name)
	}))
	values["ReadDir"] = vm.FromReflect(reflect.ValueOf(func(name string) ([]os.DirEntry, error) {
		if sub, ok := modfsSub(name); ok {
			return fs.ReadDir(moduleFS, sub)
		}
		return os.ReadDir(name)
	}))
	values["Stat"] = vm.FromReflect(reflect.ValueOf(func(name string) (os.FileInfo, error) {
		if sub, ok := modfsSub(name); ok {
			return fs.Stat(moduleFS, sub)
		}
		return os.Stat(name)
	}))
}
