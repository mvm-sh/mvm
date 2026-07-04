package stdlib

import (
	"io/fs"
	"log"
	"path"
	"reflect"

	"github.com/mvm-sh/mvm/internal/derive"
)

// Interpreted log.Logger's synth out io.Writer field would erase to interface{};
// keep it iface so the value is sound once stored into native http.Server.ErrorLog.
func init() {
	derive.RegisterNativeLayout("log.Logger", reflect.TypeFor[log.Logger]())

	// Native os concretes (*os.fileStat, os.dirFS, *fs.PathError from os.Open)
	// carry these in their method sigs and must satisfy the interpreted mirror's
	// asserts; reuse the host rtypes or Implements fails on the shadow identities.
	for _, rt := range []reflect.Type{
		reflect.TypeFor[fs.PathError](),
		reflect.TypeFor[fs.FileMode](),
		reflect.TypeFor[fs.FS](),
		reflect.TypeFor[fs.File](),
		reflect.TypeFor[fs.FileInfo](),
		reflect.TypeFor[fs.DirEntry](),
		reflect.TypeFor[fs.ReadDirFile](),
		reflect.TypeFor[fs.ReadDirFS](),
		reflect.TypeFor[fs.ReadFileFS](),
		reflect.TypeFor[fs.ReadLinkFS](),
		reflect.TypeFor[fs.StatFS](),
		reflect.TypeFor[fs.SubFS](),
		reflect.TypeFor[fs.GlobFS](),
	} {
		derive.RegisterNativeIdentity(path.Base(rt.PkgPath())+"."+rt.Name(), rt)
	}
}
