package stdlib

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/mvm-sh/mvm/vm"
)

func TestReadFileModfs(t *testing.T) {
	prev := moduleFS
	t.Cleanup(func() { moduleFS = prev })

	moduleFS = fstest.MapFS{"example.com/m/testdata/a.txt": {Data: []byte("hello")}}

	// A modfs/ path resolves against the module FS.
	got, err := ReadFileModfs("modfs/example.com/m/testdata/a.txt")
	if err != nil {
		t.Fatalf("ReadFileModfs(modfs path) error: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("ReadFileModfs(modfs path) = %q, want %q", got, "hello")
	}

	// A real on-disk path falls through to the host os.
	real := filepath.Join(t.TempDir(), "real.txt")
	if err := os.WriteFile(real, []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFileModfs(real); err != nil || string(got) != "world" {
		t.Errorf("ReadFileModfs(real path) = %q, %v; want %q, nil", got, err, "world")
	}

	// With no module FS registered, a modfs/ path falls through to os (and
	// fails as a missing host path) rather than panicking.
	moduleFS = nil
	if _, err := ReadFileModfs("modfs/anything"); err == nil {
		t.Errorf("ReadFileModfs with nil moduleFS: want host-os error, got nil")
	}
}

// TestPatchTLSModfs verifies the LoadX509KeyPair shim reads a cert/key pair
// through a modfs/ path -- the native bridged LoadX509KeyPair would call the
// host os and miss the in-memory module file.
func TestPatchTLSModfs(t *testing.T) {
	prev := moduleFS
	t.Cleanup(func() { moduleFS = prev })

	certPEM, keyPEM := genCertKey(t)
	moduleFS = fstest.MapFS{
		"example.com/m/testdata/cert.pem": {Data: certPEM},
		"example.com/m/testdata/key.pem":  {Data: keyPEM},
	}

	values := map[string]vm.Value{}
	patchTLSModfs(nil, values)
	load, ok := values["LoadX509KeyPair"].Reflect().Interface().(func(string, string) (tls.Certificate, error))
	if !ok {
		t.Fatalf("patchTLSModfs did not install a LoadX509KeyPair(string,string) shim")
	}

	cert, err := load("modfs/example.com/m/testdata/cert.pem", "modfs/example.com/m/testdata/key.pem")
	if err != nil {
		t.Fatalf("shimmed LoadX509KeyPair(modfs paths) error: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Errorf("shimmed LoadX509KeyPair returned an empty certificate chain")
	}
}

func genCertKey(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	var c, k bytes.Buffer
	_ = pem.Encode(&c, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	_ = pem.Encode(&k, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return c.Bytes(), k.Bytes()
}
