package stdlib

import (
	"io/fs"
	"log"
	"reflect"

	"github.com/mvm-sh/mvm/internal/derive"
)

// Interpreted log.Logger's synth out io.Writer field would erase to interface{};
// keep it iface so the value is sound once stored into native http.Server.ErrorLog.
func init() {
	derive.RegisterNativeLayout("log.Logger", reflect.TypeFor[log.Logger]())

	// os.Open returns a native *fs.PathError; the io/fs mirror interprets the type.
	// Share one rtype so errors.As / reflect identity reconcile (wasm).
	derive.RegisterNativeIdentity("fs.PathError", reflect.TypeFor[fs.PathError]())
}
