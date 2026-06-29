package stdlib

import (
	"errors"
	"fmt"
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/runtype"
	"github.com/mvm-sh/mvm/stdlib/stubs"
	"github.com/mvm-sh/mvm/vm"
)

// Generic synth method shapes: signatures classified by structural kind alone
// (string/[]byte/error/any/int/bool/fmt.State), not by any specific stdlib
// package. They moved out of vm with the package-specific shapes so vm core holds
// no synth-shape knowledge at all; vm only re-enters the interpreter via
// vm.SynthCall.Invoke. Recognized shapes:
//
//	S1:  func() string
//	S2:  func() ([]byte, error)
//	S3:  func([]byte) error
//	S4:  func(error) bool          (errors.Is)
//	S5:  func(any) bool            (errors.As)
//	S6:  func() error              (single-error Unwrap)
//	S7:  func() []error            (multi-error Unwrap)
//	S8:  func() int                (sort.Interface.Len)
//	S9:  func(int, int) bool       (sort.Interface.Less)
//	S10: func(int, int)            (sort.Interface.Swap)
//	S11: func(any)                 (heap.Interface.Push)
//	S12: func() any                (heap.Interface.Pop)
//	S13: func([]byte) (int, error) (io.Reader.Read / io.Writer.Write)
//	S14: func(fmt.State, rune)     (fmt.Formatter.Format)
//	S17: func() (int, bool)
//	S18: func(int) bool
//	S19: func(fmt.ScanState, rune) error (fmt.Scanner.Scan)
//	S20: func(string) error
//	S21: func() bool
//	S37: func() (rune, int, error) (io.RuneReader.ReadRune)
//	S38: func()

var (
	anyIface          = reflect.TypeFor[any]()
	errorSliceType    = reflect.TypeFor[[]error]()
	fmtStateIface     = reflect.TypeFor[fmt.State]()
	fmtScanStateIface = reflect.TypeFor[fmt.ScanState]()
)

func isByteSlice(t reflect.Type) bool  { return t == byteSliceType }
func isAnyType(t reflect.Type) bool    { return t == anyIface }
func isErrorSlice(t reflect.Type) bool { return t == errorSliceType }

// detectGenericShape classifies sig into a structural shape, run before the
// package-specific classifier (the two are disjoint). sig is a func type.
func detectGenericShape(sig reflect.Type) (stubs.Shape, bool) {
	nin, nout := sig.NumIn(), sig.NumOut()
	switch {
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.String:
		return stubs.ShapeS1, true
	case nin == 0 && nout == 1 && isErr(sig.Out(0)):
		return stubs.ShapeS6, true
	case nin == 0 && nout == 1 && isErrorSlice(sig.Out(0)):
		return stubs.ShapeS7, true
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.Int:
		return stubs.ShapeS8, true
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS21, true
	case nin == 0 && nout == 1 && isAnyType(sig.Out(0)):
		return stubs.ShapeS12, true
	case nin == 0 && nout == 2 &&
		isByteSlice(sig.Out(0)) && isErr(sig.Out(1)):
		return stubs.ShapeS2, true
	case nin == 0 && nout == 2 &&
		sig.Out(0).Kind() == reflect.Int && sig.Out(1).Kind() == reflect.Bool:
		return stubs.ShapeS17, true
	case nin == 1 && nout == 1 &&
		sig.In(0).Kind() == reflect.Int && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS18, true
	case nin == 1 && nout == 1 &&
		isByteSlice(sig.In(0)) && isErr(sig.Out(0)):
		return stubs.ShapeS3, true
	case nin == 1 && nout == 1 &&
		sig.In(0).Kind() == reflect.String && isErr(sig.Out(0)):
		return stubs.ShapeS20, true
	case nin == 1 && nout == 1 &&
		isErr(sig.In(0)) && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS4, true
	case nin == 1 && nout == 1 &&
		isAnyType(sig.In(0)) && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS5, true
	case nin == 1 && nout == 0 && isAnyType(sig.In(0)):
		return stubs.ShapeS11, true
	case nin == 1 && nout == 2 && isByteSlice(sig.In(0)) &&
		sig.Out(0).Kind() == reflect.Int && isErr(sig.Out(1)):
		return stubs.ShapeS13, true
	case nin == 0 && nout == 3 && sig.Out(0).Kind() == reflect.Int32 &&
		sig.Out(1).Kind() == reflect.Int && isErr(sig.Out(2)):
		return stubs.ShapeS37, true
	case nin == 0 && nout == 0:
		return stubs.ShapeS38, true
	case nin == 2 && nout == 1 &&
		sig.In(0).Kind() == reflect.Int && sig.In(1).Kind() == reflect.Int &&
		sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS9, true
	case nin == 2 && nout == 0 &&
		sig.In(0).Kind() == reflect.Int && sig.In(1).Kind() == reflect.Int:
		return stubs.ShapeS10, true
	case nin == 2 && nout == 0 &&
		sig.In(0) == fmtStateIface && sig.In(1).Kind() == reflect.Int32:
		return stubs.ShapeS14, true
	case nin == 2 && nout == 1 && sig.In(0) == fmtScanStateIface &&
		sig.In(1).Kind() == reflect.Int32 && isErr(sig.Out(0)):
		return stubs.ShapeS19, true
	}
	return 0, false
}

// raiseMethodErr panics with err, re-raising an interpreter PanicError as its raw
// value so a native recover sees the original.
func raiseMethodErr(err error) {
	vm.RaiseIfInterpPanic(err)
	panic(err)
}

func reflectToErrorSlice(v reflect.Value) []error {
	v = runtype.Exportable(v)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.IsNil() {
		return nil
	}
	if res, ok := v.Interface().([]error); ok {
		return res
	}
	res := make([]error, v.Len())
	for i := range res {
		res[i] = vm.ReflectToError(v.Index(i))
	}
	return res
}

// makeGenericHandler builds the native callback for a generic shape, or nil if
// shape is not generic (a package-specific shape handled by makeStdlibHandler).
// Each closure re-enters the interpreter via call.Invoke, then marshals results.
func makeGenericHandler(call vm.SynthCall, shape stubs.Shape) any {
	switch shape {
	case stubs.ShapeS1:
		return func(recv unsafe.Pointer) string {
			out, err := call.Invoke(recv, nil)
			if err != nil {
				raiseMethodErr(err)
			}
			if len(out) != 1 {
				return ""
			}
			return out[0].String()
		}
	case stubs.ShapeS2:
		return func(recv unsafe.Pointer) ([]byte, error) {
			out, err := call.Invoke(recv, nil)
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S2 dispatch produced wrong arity")
			}
			var data []byte
			if out[0].IsValid() && (out[0].Kind() != reflect.Slice || !out[0].IsNil()) {
				data = out[0].Bytes()
			}
			return data, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS3:
		return func(recv unsafe.Pointer, data []byte) error {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(data)})
			if err != nil {
				return err
			}
			if len(out) != 1 {
				return errors.New("synth: S3 dispatch produced wrong arity")
			}
			return vm.ReflectToError(out[0])
		}
	case stubs.ShapeS4:
		return func(recv unsafe.Pointer, target error) bool {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(&target).Elem()})
			if err != nil || len(out) != 1 {
				return false
			}
			return out[0].Bool()
		}
	case stubs.ShapeS5:
		return func(recv unsafe.Pointer, target any) bool {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(&target).Elem()})
			if err != nil || len(out) != 1 {
				return false
			}
			return out[0].Bool()
		}
	case stubs.ShapeS6:
		return func(recv unsafe.Pointer) error {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return nil
			}
			return vm.ReflectToError(out[0])
		}
	case stubs.ShapeS7:
		return func(recv unsafe.Pointer) []error {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return nil
			}
			return reflectToErrorSlice(out[0])
		}
	case stubs.ShapeS8:
		return func(recv unsafe.Pointer) int {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return 0
			}
			return int(out[0].Int())
		}
	case stubs.ShapeS9:
		return func(recv unsafe.Pointer, i, j int) bool {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)})
			if err != nil || len(out) != 1 {
				return false
			}
			return out[0].Bool()
		}
	case stubs.ShapeS10:
		return func(recv unsafe.Pointer, i, j int) {
			_, _ = call.Invoke(recv, []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)})
		}
	case stubs.ShapeS11:
		return func(recv unsafe.Pointer, x any) {
			_, _ = call.Invoke(recv, []reflect.Value{reflect.ValueOf(&x).Elem()})
		}
	case stubs.ShapeS12:
		return func(recv unsafe.Pointer) any {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 || !out[0].IsValid() {
				return nil
			}
			return runtype.Exportable(out[0]).Interface()
		}
	case stubs.ShapeS13:
		return func(recv unsafe.Pointer, p []byte) (int, error) {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(p)})
			if err != nil {
				vm.RaiseIfInterpPanic(err)
				return 0, err
			}
			if len(out) != 2 {
				return 0, errors.New("synth: S13 dispatch produced wrong arity")
			}
			return int(out[0].Int()), vm.ReflectToError(out[1])
		}
	case stubs.ShapeS14:
		return func(recv unsafe.Pointer, st fmt.State, verb rune) {
			_, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(&st).Elem(), reflect.ValueOf(verb)})
			if err != nil {
				raiseMethodErr(err)
			}
		}
	case stubs.ShapeS17:
		return func(recv unsafe.Pointer) (int, bool) {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 2 {
				return 0, false
			}
			return int(out[0].Int()), out[1].Bool()
		}
	case stubs.ShapeS18:
		return func(recv unsafe.Pointer, c int) bool {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(c)})
			if err != nil || len(out) != 1 {
				return false
			}
			return out[0].Bool()
		}
	case stubs.ShapeS19:
		return func(recv unsafe.Pointer, st fmt.ScanState, verb rune) error {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(&st).Elem(), reflect.ValueOf(verb)})
			if err != nil {
				return err
			}
			if len(out) != 1 {
				return errors.New("synth: S19 dispatch produced wrong arity")
			}
			return vm.ReflectToError(out[0])
		}
	case stubs.ShapeS20:
		return func(recv unsafe.Pointer, value string) error {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(value)})
			if err != nil {
				return err
			}
			if len(out) != 1 {
				return errors.New("synth: S20 dispatch produced wrong arity")
			}
			return vm.ReflectToError(out[0])
		}
	case stubs.ShapeS21:
		return func(recv unsafe.Pointer) bool {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return false
			}
			return out[0].Bool()
		}
	case stubs.ShapeS37:
		return func(recv unsafe.Pointer) (rune, int, error) {
			out, err := call.Invoke(recv, nil)
			if err != nil {
				vm.RaiseIfInterpPanic(err)
				return 0, 0, err
			}
			if len(out) != 3 {
				return 0, 0, errors.New("synth: S37 dispatch produced wrong arity")
			}
			return rune(out[0].Int()), int(out[1].Int()), vm.ReflectToError(out[2])
		}
	case stubs.ShapeS38:
		return func(recv unsafe.Pointer) {
			_, err := call.Invoke(recv, nil)
			if err != nil {
				raiseMethodErr(err)
			}
		}
	}
	return nil
}
