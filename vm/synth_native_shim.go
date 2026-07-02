package vm

import (
	"fmt"
	"io"
	"reflect"
)

// On the shared-PC (wasm) build a synth method's text PC is the -1 unreachable
// sentinel, so native code cannot dispatch it (MODE 2). When a synth value
// implementing error/Stringer crosses into a native interface sink -- e.g.
// testing's native fmt.Sprintf -- wrap it in a native shim whose method has a
// real PC and re-enters the interpreter via callMethod.

type synthErrShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthErrShim) Error() string { return s.m.callSynthString(s.rv, "Error") }

type synthStrShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthStrShim) String() string { return s.m.callSynthString(s.rv, "String") }

// On wasm a synth value's Read/Write PC is the -1 unreachable sentinel, so a
// native sink (io.Copy, os.File.ReadFrom) dispatching it via itab traps; these
// shims forward through the vm instead.
type synthReaderShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthReaderShim) Read(p []byte) (int, error) { return s.m.callSynthRW(s.rv, "Read", p) }

type synthWriterShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthWriterShim) Write(p []byte) (int, error) { return s.m.callSynthRW(s.rv, "Write", p) }

// For the interpreted fmt.pp handed to a native Formatter (e.g. big.Int).
type synthFmtStateShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthFmtStateShim) Write(p []byte) (int, error) { return s.m.callSynthRW(s.rv, "Write", p) }
func (s synthFmtStateShim) Width() (int, bool)          { return s.m.callSynthIntBool(s.rv, "Width") }
func (s synthFmtStateShim) Precision() (int, bool) {
	return s.m.callSynthIntBool(s.rv, "Precision")
}

func (s synthFmtStateShim) Flag(c int) bool {
	out, err := s.m.callSynth(s.rv, "Flag", []reflect.Value{reflect.ValueOf(c)})
	return err == nil && len(out) == 1 && out[0].Bool()
}

// For the interpreted fmt.ss handed to a native Scanner (e.g. big.Rat).
type synthScanStateShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthScanStateShim) ReadRune() (rune, int, error) {
	out, err := s.m.callSynth(s.rv, "ReadRune", nil)
	if err != nil || len(out) != 3 {
		return 0, 0, err
	}
	e, _ := out[2].Interface().(error)
	return rune(out[0].Int()), int(out[1].Int()), e
}

func (s synthScanStateShim) UnreadRune() error {
	out, err := s.m.callSynth(s.rv, "UnreadRune", nil)
	if err != nil {
		return err
	}
	if len(out) == 1 {
		e, _ := out[0].Interface().(error)
		return e
	}
	return nil
}

func (s synthScanStateShim) SkipSpace() {
	_, _ = s.m.callSynth(s.rv, "SkipSpace", nil)
}

func (s synthScanStateShim) Token(skipSpace bool, f func(rune) bool) ([]byte, error) {
	out, err := s.m.callSynth(s.rv, "Token",
		[]reflect.Value{reflect.ValueOf(skipSpace), reflect.ValueOf(f)})
	if err != nil || len(out) != 2 {
		return nil, err
	}
	e, _ := out[1].Interface().(error)
	b, _ := out[0].Interface().([]byte)
	return b, e
}

func (s synthScanStateShim) Width() (int, bool) { return s.m.callSynthIntBool(s.rv, "Width") }

func (s synthScanStateShim) Read(p []byte) (int, error) { return s.m.callSynthRW(s.rv, "Read", p) }

var (
	synthErrShimType       = reflect.TypeFor[synthErrShim]()
	synthStrShimType       = reflect.TypeFor[synthStrShim]()
	synthReaderShimType    = reflect.TypeFor[synthReaderShim]()
	synthWriterShimType    = reflect.TypeFor[synthWriterShim]()
	synthFmtStateShimType  = reflect.TypeFor[synthFmtStateShim]()
	synthScanStateShimType = reflect.TypeFor[synthScanStateShim]()
	stringerIface          = reflect.TypeFor[fmt.Stringer]()
	readerIface            = reflect.TypeFor[io.Reader]()
	writerIface            = reflect.TypeFor[io.Writer]()
	fmtStateIface          = reflect.TypeFor[fmt.State]()
	scanStateIface         = reflect.TypeFor[fmt.ScanState]()
)

func (m *Machine) callSynth(rv reflect.Value, name string, args []reflect.Value) ([]reflect.Value, error) {
	t := m.typeByRtype(rv.Type())
	if t == nil {
		return nil, fmt.Errorf("no vm type for %v", rv.Type())
	}
	method, ok := m.MethodByName(t, name)
	if !ok {
		return nil, fmt.Errorf("no method %s on %v", name, rv.Type())
	}
	return callMethod(m, t, name, rv, method, method.Rtype, args)
}

// p's backing is shared, so the interpreted method fills the caller's buffer.
// collectReturns has already canonicalized any returned io.EOF.
func (m *Machine) callSynthRW(rv reflect.Value, name string, p []byte) (int, error) {
	out, err := m.callSynth(rv, name, []reflect.Value{reflect.ValueOf(p)})
	if err != nil {
		return 0, err
	}
	if len(out) != 2 {
		return 0, io.ErrClosedPipe
	}
	e, _ := out[1].Interface().(error)
	return int(out[0].Int()), e
}

func (m *Machine) callSynthString(rv reflect.Value, name string) string {
	out, err := m.callSynth(rv, name, nil)
	if err != nil || len(out) == 0 {
		return ""
	}
	return out[0].String()
}

func (m *Machine) callSynthIntBool(rv reflect.Value, name string) (int, bool) {
	out, err := m.callSynth(rv, name, nil)
	if err != nil || len(out) != 2 {
		return 0, false
	}
	return int(out[0].Int()), out[1].Bool()
}

// wrapSynthIfaceForNative returns a native forwarding shim for a synth
// error/Stringer/Reader/Writer reaching a native interface target, or ok=false
// to leave the value unchanged. error wins over Stringer to match fmt's dispatch
// order. readerWriterOnly skips the error/Stringer shims, for callers where the
// native callee introspects the concrete type (errors.As, reflect) rather than
// dispatching the method.
func (m *Machine) wrapSynthIfaceForNative(val, targetType reflect.Type, rv reflect.Value, readerWriterOnly bool) (reflect.Value, bool) {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
		val = rv.Type()
	}
	if !isSynthOrSynthPtr(val) {
		return reflect.Value{}, false
	}
	if !readerWriterOnly && val.Implements(errorIface) && synthErrShimType.AssignableTo(targetType) {
		return reflect.ValueOf(synthErrShim{m, rv}), true
	}
	if !readerWriterOnly && val.Implements(stringerIface) && synthStrShimType.AssignableTo(targetType) {
		return reflect.ValueOf(synthStrShim{m, rv}), true
	}
	// The shims are AssignableTo `any`; excluding it keeps a value reaching an
	// `any` sink (e.g. sync.Pool.Put) at its concrete identity.
	if targetType.NumMethod() > 0 {
		if val.Implements(readerIface) && synthReaderShimType.AssignableTo(targetType) {
			return reflect.ValueOf(synthReaderShim{m, rv}), true
		}
		if val.Implements(writerIface) && synthWriterShimType.AssignableTo(targetType) {
			return reflect.ValueOf(synthWriterShim{m, rv}), true
		}
		if val.Implements(fmtStateIface) && synthFmtStateShimType.AssignableTo(targetType) {
			return reflect.ValueOf(synthFmtStateShim{m, rv}), true
		}
		if val.Implements(scanStateIface) && synthScanStateShimType.AssignableTo(targetType) {
			return reflect.ValueOf(synthScanStateShim{m, rv}), true
		}
	}
	return reflect.Value{}, false
}
