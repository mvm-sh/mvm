package vm

import (
	"errors"
	"io/fs"
	"reflect"
	"time"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// Handlers for the io/fs method shapes S22-S31. Each re-enters the interpreter
// via callMethod, then marshals the result(s) back to the native return types.

// ifaceResult returns v's exported value boxed as an interface, or nil when v is
// invalid (a typed-nil return).
func ifaceResult(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	return Exportable(v).Interface()
}

// makeHandlerS22 bridges S22: (T).Size() int64 (fs.FileInfo.Size).
func makeHandlerS22(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS22 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) int64 {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return 0
		}
		return out[0].Int()
	}
}

// makeHandlerS23 bridges S23: (T).Mode()/Type() fs.FileMode.
func makeHandlerS23(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS23 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) fs.FileMode {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return 0
		}
		return fs.FileMode(out[0].Uint())
	}
}

// makeHandlerS24 bridges S24: (T).ModTime() time.Time (fs.FileInfo.ModTime).
func makeHandlerS24(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS24 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) time.Time {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return time.Time{}
		}
		tt, _ := ifaceResult(out[0]).(time.Time)
		return tt
	}
}

// makeHandlerS25 bridges S25: (T).Info()/Stat() (fs.FileInfo, error).
func makeHandlerS25(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS25 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) (fs.FileInfo, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S25 dispatch produced wrong arity")
		}
		fi, _ := ifaceResult(out[0]).(fs.FileInfo)
		return fi, reflectToError(out[1])
	}
}

// makeHandlerS26 bridges S26: (T).Open(string) (fs.File, error) (fs.FS.Open).
func makeHandlerS26(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS26 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, arg string) (fs.File, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, []reflect.Value{reflect.ValueOf(arg)})
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S26 dispatch produced wrong arity")
		}
		f, _ := ifaceResult(out[0]).(fs.File)
		return f, reflectToError(out[1])
	}
}

// makeHandlerS27 bridges S27: (T).Stat(string) (fs.FileInfo, error) (fs.StatFS.Stat).
func makeHandlerS27(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS27 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, arg string) (fs.FileInfo, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, []reflect.Value{reflect.ValueOf(arg)})
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S27 dispatch produced wrong arity")
		}
		fi, _ := ifaceResult(out[0]).(fs.FileInfo)
		return fi, reflectToError(out[1])
	}
}

// makeHandlerS28 bridges S28: (T).Sub(string) (fs.FS, error) (fs.SubFS.Sub).
func makeHandlerS28(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS28 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, arg string) (fs.FS, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, []reflect.Value{reflect.ValueOf(arg)})
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S28 dispatch produced wrong arity")
		}
		sub, _ := ifaceResult(out[0]).(fs.FS)
		return sub, reflectToError(out[1])
	}
}

// makeHandlerS29 bridges S29: (T).Glob(string) ([]string, error) (fs.GlobFS.Glob).
func makeHandlerS29(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS29 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, arg string) ([]string, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, []reflect.Value{reflect.ValueOf(arg)})
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S29 dispatch produced wrong arity")
		}
		ss, _ := ifaceResult(out[0]).([]string)
		return ss, reflectToError(out[1])
	}
}

// makeHandlerS30 bridges S30: (T).ReadDir(string) ([]fs.DirEntry, error) (fs.ReadDirFS.ReadDir).
func makeHandlerS30(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS30 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, arg string) ([]fs.DirEntry, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, []reflect.Value{reflect.ValueOf(arg)})
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S30 dispatch produced wrong arity")
		}
		de, _ := ifaceResult(out[0]).([]fs.DirEntry)
		return de, reflectToError(out[1])
	}
}

// makeHandlerS31 bridges S31: (T).ReadFile(string) ([]byte, error) (fs.ReadFileFS.ReadFile).
func makeHandlerS31(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS31 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, arg string) ([]byte, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, []reflect.Value{reflect.ValueOf(arg)})
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S31 dispatch produced wrong arity")
		}
		b, _ := ifaceResult(out[0]).([]byte)
		return b, reflectToError(out[1])
	}
}
