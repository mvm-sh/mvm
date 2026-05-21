// Package gobx bridges interpreted types that implement
// encoding.BinaryMarshaler/BinaryUnmarshaler through native encoding/gob.
//
// gob inspects a value's reflect type for those interfaces, but an interpreted
// value reaches (*gob.Encoder).Encode / (*gob.Decoder).Decode as a synthetic
// struct via an `any` parameter, where the default any-bridging only exposes
// display methods. These arg proxies wrap the mvm Iface in a host type that
// implements BinaryMarshaler/BinaryUnmarshaler and dispatches the interpreted
// method back through the VM. gob does not name-match external-encoder types on
// decode, so distinct encode/decode proxy types round-trip. Mirrors stdlib/jsonx.
package gobx

import (
	"encoding/gob"
	"fmt"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

func init() {
	vm.RegisterArgProxyMethod((*gob.Encoder)(nil), "Encode", 0, newEncodeProxy)
	vm.RegisterArgProxyMethod((*gob.Decoder)(nil), "Decode", 0, newDecodeProxy)
}

var (
	marshalFnType   = reflect.TypeOf((func() ([]byte, error))(nil))
	unmarshalFnType = reflect.TypeOf((func([]byte) error)(nil))
)

// binaryMarshalProxy implements encoding.BinaryMarshaler; MarshalBinary
// dispatches the interpreted type's MarshalBinary back through the VM.
type binaryMarshalProxy struct {
	m   *vm.Machine
	ifc vm.Iface
}

func (p *binaryMarshalProxy) MarshalBinary() ([]byte, error) {
	method, ok := p.m.MethodByName(p.ifc.Typ, "MarshalBinary")
	if !ok {
		return nil, fmt.Errorf("gobx: %s has no MarshalBinary method", p.ifc.Typ.Name)
	}
	fval := p.m.MakeMethodCallable(p.ifc, method)
	out, err := p.m.CallFunc(fval, marshalFnType, nil)
	if err != nil {
		return nil, err
	}
	if len(out) != 2 {
		return nil, fmt.Errorf("MarshalBinary: expected 2 returns, got %d", len(out))
	}
	var data []byte
	if out[0].IsValid() && !out[0].IsZero() {
		data = out[0].Bytes()
	}
	if out[1].IsValid() && !out[1].IsNil() {
		if e, eok := out[1].Interface().(error); eok {
			return data, e
		}
	}
	return data, nil
}

// binaryUnmarshalProxy implements encoding.BinaryUnmarshaler; UnmarshalBinary
// dispatches the interpreted (pointer-receiver) UnmarshalBinary, mutating the
// wrapped interpreted value in place.
type binaryUnmarshalProxy struct {
	m   *vm.Machine
	ifc vm.Iface
}

func (p *binaryUnmarshalProxy) UnmarshalBinary(data []byte) error {
	method, ok := p.m.MethodByName(p.ifc.Typ, "UnmarshalBinary")
	if !ok {
		return fmt.Errorf("gobx: %s has no UnmarshalBinary method", p.ifc.Typ.Name)
	}
	fval := p.m.MakeMethodCallable(p.ifc, method)
	out, err := p.m.CallFunc(fval, unmarshalFnType, []reflect.Value{reflect.ValueOf(data)})
	if err != nil {
		return err
	}
	if len(out) != 1 {
		return fmt.Errorf("UnmarshalBinary: expected 1 return, got %d", len(out))
	}
	if out[0].IsValid() && !out[0].IsNil() {
		if e, eok := out[0].Interface().(error); eok {
			return e
		}
	}
	return nil
}

// newEncodeProxy wraps the value only when it implements MarshalBinary;
// otherwise it falls back to default any-bridging, preserving current behavior.
func newEncodeProxy(m *vm.Machine, ifc vm.Iface) reflect.Value {
	if ifc.Typ != nil {
		if _, ok := m.MethodByName(ifc.Typ, "MarshalBinary"); ok {
			return reflect.ValueOf(&binaryMarshalProxy{m: m, ifc: ifc})
		}
	}
	return m.BridgeForAny(ifc)
}

func newDecodeProxy(m *vm.Machine, ifc vm.Iface) reflect.Value {
	if ifc.Typ != nil {
		if _, ok := m.MethodByName(ifc.Typ, "UnmarshalBinary"); ok {
			return reflect.ValueOf(&binaryUnmarshalProxy{m: m, ifc: ifc})
		}
	}
	return m.BridgeForAny(ifc)
}
