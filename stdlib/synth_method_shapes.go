package stdlib

import (
	"context"
	"encoding/xml"
	"errors"
	"io/fs"
	"log/slog"
	"reflect"
	"time"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
	"github.com/mvm-sh/mvm/vm"
)

// Synth method shapes whose native callback types name a specific stdlib package
// (io/fs, log/slog, encoding/xml). Registering them here keeps vm core free of
// those imports; the generic shapes stay in vm.
func init() {
	vm.RegisterExtendedShapes(detectExtendedShape, makeExtendedHandler)
}

var (
	errorIface = reflect.TypeFor[error]()

	xmlEncoderPtr = reflect.TypeFor[*xml.Encoder]()
	xmlDecoderPtr = reflect.TypeFor[*xml.Decoder]()
	xmlStartElem  = reflect.TypeFor[xml.StartElement]()

	byteSliceType     = reflect.TypeFor[[]byte]()
	stringSliceType   = reflect.TypeFor[[]string]()
	fsFileModeType    = reflect.TypeFor[fs.FileMode]()
	timeTimeType      = reflect.TypeFor[time.Time]()
	fsFileInfoIface   = reflect.TypeFor[fs.FileInfo]()
	fsFileIface       = reflect.TypeFor[fs.File]()
	fsFSIface         = reflect.TypeFor[fs.FS]()
	dirEntrySliceType = reflect.TypeFor[[]fs.DirEntry]()

	contextIface      = reflect.TypeFor[context.Context]()
	slogLevelType     = reflect.TypeFor[slog.Level]()
	slogRecordType    = reflect.TypeFor[slog.Record]()
	slogAttrSliceType = reflect.TypeFor[[]slog.Attr]()
	slogHandlerIface  = reflect.TypeFor[slog.Handler]()
	slogValueType     = reflect.TypeFor[slog.Value]()
)

func isErr(t reflect.Type) bool { return t == errorIface }

// detectExtendedShape classifies a method signature into one of the stdlib-specific
// shapes, run only after vm's generic classifier fails (the two are disjoint).
func detectExtendedShape(sig reflect.Type) (stubs.Shape, bool) {
	nin, nout := sig.NumIn(), sig.NumOut()
	switch {
	// encoding/xml cluster.
	case nin == 2 && nout == 1 && sig.In(0) == xmlEncoderPtr &&
		sig.In(1) == xmlStartElem && isErr(sig.Out(0)):
		return stubs.ShapeS15, true
	case nin == 2 && nout == 1 && sig.In(0) == xmlDecoderPtr &&
		sig.In(1) == xmlStartElem && isErr(sig.Out(0)):
		return stubs.ShapeS16, true

	// io/fs cluster.
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.Int64:
		return stubs.ShapeS22, true
	case nin == 0 && nout == 1 && sig.Out(0) == fsFileModeType:
		return stubs.ShapeS23, true
	case nin == 0 && nout == 1 && sig.Out(0) == timeTimeType:
		return stubs.ShapeS24, true
	case nin == 0 && nout == 2 && sig.Out(0) == fsFileInfoIface && isErr(sig.Out(1)):
		return stubs.ShapeS25, true
	case nin == 1 && nout == 2 && sig.In(0).Kind() == reflect.String && isErr(sig.Out(1)):
		switch sig.Out(0) {
		case fsFileIface:
			return stubs.ShapeS26, true
		case fsFileInfoIface:
			return stubs.ShapeS27, true
		case fsFSIface:
			return stubs.ShapeS28, true
		case stringSliceType:
			return stubs.ShapeS29, true
		case dirEntrySliceType:
			return stubs.ShapeS30, true
		case byteSliceType:
			return stubs.ShapeS31, true
		}

	// log/slog cluster (slog.Handler).
	case nin == 2 && nout == 1 && sig.In(0) == contextIface &&
		sig.In(1) == slogLevelType && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS32, true
	case nin == 2 && nout == 1 && sig.In(0) == contextIface &&
		sig.In(1) == slogRecordType && isErr(sig.Out(0)):
		return stubs.ShapeS33, true
	case nin == 1 && nout == 1 && sig.In(0) == slogAttrSliceType &&
		sig.Out(0) == slogHandlerIface:
		return stubs.ShapeS34, true
	case nin == 1 && nout == 1 && sig.In(0).Kind() == reflect.String &&
		sig.Out(0) == slogHandlerIface:
		return stubs.ShapeS35, true
	case nin == 0 && nout == 1 && sig.Out(0) == slogValueType:
		return stubs.ShapeS36, true
	}
	return 0, false
}

// ifaceResult boxes v's exported value as an interface, nil when v is invalid.
func ifaceResult(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	return vm.Exportable(v).Interface()
}

// ctxArg boxes ctx as an interface-typed reflect value, keeping the interface
// type even for a nil ctx (ValueOf would yield an invalid Value).
func ctxArg(ctx context.Context) reflect.Value {
	return reflect.ValueOf(&ctx).Elem()
}

// makeExtendedHandler builds the native callback for a classified extended shape.
// Each closure re-enters the interpreter via call.Invoke, then marshals the
// result(s) back to the native return types.
func makeExtendedHandler(call vm.SynthCall, shape stubs.Shape) any {
	switch shape {
	case stubs.ShapeS15: // (T).MarshalXML(*xml.Encoder, xml.StartElement) error
		return func(recv unsafe.Pointer, e *xml.Encoder, start xml.StartElement) error {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(e), reflect.ValueOf(start)})
			if err != nil {
				return err
			}
			if len(out) != 1 {
				return errors.New("synth: S15 dispatch produced wrong arity")
			}
			return vm.ReflectToError(out[0])
		}
	case stubs.ShapeS16: // (T).UnmarshalXML(*xml.Decoder, xml.StartElement) error
		return func(recv unsafe.Pointer, d *xml.Decoder, start xml.StartElement) error {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(d), reflect.ValueOf(start)})
			if err != nil {
				return err
			}
			if len(out) != 1 {
				return errors.New("synth: S16 dispatch produced wrong arity")
			}
			return vm.ReflectToError(out[0])
		}
	case stubs.ShapeS22: // (T).Size() int64
		return func(recv unsafe.Pointer) int64 {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return 0
			}
			return out[0].Int()
		}
	case stubs.ShapeS23: // (T).Mode()/Type() fs.FileMode
		return func(recv unsafe.Pointer) fs.FileMode {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return 0
			}
			return fs.FileMode(out[0].Uint())
		}
	case stubs.ShapeS24: // (T).ModTime() time.Time
		return func(recv unsafe.Pointer) time.Time {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return time.Time{}
			}
			tt, _ := ifaceResult(out[0]).(time.Time)
			return tt
		}
	case stubs.ShapeS25: // (T).Info()/Stat() (fs.FileInfo, error)
		return func(recv unsafe.Pointer) (fs.FileInfo, error) {
			out, err := call.Invoke(recv, nil)
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S25 dispatch produced wrong arity")
			}
			fi, _ := ifaceResult(out[0]).(fs.FileInfo)
			return fi, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS26: // (T).Open(string) (fs.File, error)
		return func(recv unsafe.Pointer, arg string) (fs.File, error) {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(arg)})
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S26 dispatch produced wrong arity")
			}
			f, _ := ifaceResult(out[0]).(fs.File)
			return f, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS27: // (T).Stat(string) (fs.FileInfo, error)
		return func(recv unsafe.Pointer, arg string) (fs.FileInfo, error) {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(arg)})
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S27 dispatch produced wrong arity")
			}
			fi, _ := ifaceResult(out[0]).(fs.FileInfo)
			return fi, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS28: // (T).Sub(string) (fs.FS, error)
		return func(recv unsafe.Pointer, arg string) (fs.FS, error) {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(arg)})
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S28 dispatch produced wrong arity")
			}
			sub, _ := ifaceResult(out[0]).(fs.FS)
			return sub, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS29: // (T).Glob(string) ([]string, error)
		return func(recv unsafe.Pointer, arg string) ([]string, error) {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(arg)})
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S29 dispatch produced wrong arity")
			}
			ss, _ := ifaceResult(out[0]).([]string)
			return ss, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS30: // (T).ReadDir(string) ([]fs.DirEntry, error)
		return func(recv unsafe.Pointer, arg string) ([]fs.DirEntry, error) {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(arg)})
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S30 dispatch produced wrong arity")
			}
			de, _ := ifaceResult(out[0]).([]fs.DirEntry)
			return de, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS31: // (T).ReadFile(string) ([]byte, error)
		return func(recv unsafe.Pointer, arg string) ([]byte, error) {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(arg)})
			if err != nil {
				return nil, err
			}
			if len(out) != 2 {
				return nil, errors.New("synth: S31 dispatch produced wrong arity")
			}
			b, _ := ifaceResult(out[0]).([]byte)
			return b, vm.ReflectToError(out[1])
		}
	case stubs.ShapeS32: // (T).Enabled(context.Context, slog.Level) bool
		return func(recv unsafe.Pointer, ctx context.Context, level slog.Level) bool {
			out, err := call.Invoke(recv, []reflect.Value{ctxArg(ctx), reflect.ValueOf(level)})
			if err != nil || len(out) != 1 {
				return false
			}
			return out[0].Bool()
		}
	case stubs.ShapeS33: // (T).Handle(context.Context, slog.Record) error
		return func(recv unsafe.Pointer, ctx context.Context, record slog.Record) error {
			out, err := call.Invoke(recv, []reflect.Value{ctxArg(ctx), reflect.ValueOf(record)})
			if err != nil {
				return err
			}
			if len(out) != 1 {
				return errors.New("synth: S33 dispatch produced wrong arity")
			}
			return vm.ReflectToError(out[0])
		}
	case stubs.ShapeS34: // (T).WithAttrs([]slog.Attr) slog.Handler
		return func(recv unsafe.Pointer, attrs []slog.Attr) slog.Handler {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(attrs)})
			if err != nil || len(out) != 1 {
				return nil
			}
			h, _ := ifaceResult(out[0]).(slog.Handler)
			return h
		}
	case stubs.ShapeS35: // (T).WithGroup(string) slog.Handler
		return func(recv unsafe.Pointer, name string) slog.Handler {
			out, err := call.Invoke(recv, []reflect.Value{reflect.ValueOf(name)})
			if err != nil || len(out) != 1 {
				return nil
			}
			h, _ := ifaceResult(out[0]).(slog.Handler)
			return h
		}
	case stubs.ShapeS36: // (T).LogValue() slog.Value
		return func(recv unsafe.Pointer) slog.Value {
			out, err := call.Invoke(recv, nil)
			if err != nil || len(out) != 1 {
				return slog.Value{}
			}
			v, _ := ifaceResult(out[0]).(slog.Value)
			return v
		}
	}
	return nil
}
