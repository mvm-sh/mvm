package stdlib

import (
	"crypto/tls"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

func init() {
	RegisterPackagePatcher("crypto/tls", patchTLSModfs)
}

// patchTLSModfs makes crypto/tls.LoadX509KeyPair modfs-aware. The bridged
// native LoadX509KeyPair reads the cert and key with the host os, so a
// "modfs/" path produced by virtualized runtime.Caller (e.g. grpc's
// testdata.Path) fails. The shim reads both files through ReadFileModfs and
// hands the bytes to the native, file-less tls.X509KeyPair -- exactly what the
// real LoadX509KeyPair does.
func patchTLSModfs(_ *vm.Machine, values map[string]vm.Value) {
	values["LoadX509KeyPair"] = vm.FromReflect(reflect.ValueOf(func(certFile, keyFile string) (tls.Certificate, error) {
		certPEM, err := ReadFileModfs(certFile)
		if err != nil {
			return tls.Certificate{}, err
		}
		keyPEM, err := ReadFileModfs(keyFile)
		if err != nil {
			return tls.Certificate{}, err
		}
		return tls.X509KeyPair(certPEM, keyPEM)
	}))
}
