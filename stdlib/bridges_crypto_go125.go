//go:build go1.25

package stdlib

import (
	"crypto"
	"io"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// BridgeSigner bridges crypto.Signer (Public, Sign) so an interpreted
// private key type can be passed to native APIs that accept a Signer.
type BridgeSigner struct {
	FnPublic func() crypto.PublicKey
	FnSign   func(io.Reader, []byte, crypto.SignerOpts) ([]byte, error)
	Val      any
	Ifc      vm.Iface
}

// Public implements crypto.Signer.
func (b *BridgeSigner) Public() crypto.PublicKey { return b.FnPublic() }

// Sign implements crypto.Signer.
func (b *BridgeSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return b.FnSign(rand, digest, opts)
}

// BridgeMessageSigner bridges crypto.MessageSigner, which is crypto.Signer
// plus SignMessage. It embeds BridgeSigner (Public/Sign + Val/Ifc) and adds
// only SignMessage, mirroring how BridgeHeapInterface embeds
// BridgeSortInterface. Registered as a richer bridge than BridgeSigner so a
// value that implements SignMessage keeps that capability when passed to a
// crypto.Signer parameter; crypto.SignMessage upgrades via signer.(MessageSigner).
type BridgeMessageSigner struct {
	BridgeSigner
	FnSignMessage func(io.Reader, []byte, crypto.SignerOpts) ([]byte, error)
}

// SignMessage implements crypto.MessageSigner.
func (b *BridgeMessageSigner) SignMessage(rand io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	return b.FnSignMessage(rand, msg, opts)
}

func init() {
	vm.InterfaceBridges[reflect.TypeOf((*crypto.Signer)(nil)).Elem()] = reflect.TypeOf((*BridgeSigner)(nil))
	vm.InterfaceBridges[reflect.TypeOf((*crypto.MessageSigner)(nil)).Elem()] = reflect.TypeOf((*BridgeMessageSigner)(nil))
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeSigner)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeMessageSigner)(nil))] = true
}
