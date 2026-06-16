package stdlib

import (
	"log"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// Interpreted log.Logger's synth out io.Writer field would erase to interface{};
// keep it iface so the value is sound once stored into native http.Server.ErrorLog.
func init() {
	vm.RegisterNativeLayout("log.Logger", reflect.TypeFor[log.Logger]())
}
