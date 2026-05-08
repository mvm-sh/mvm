package stdlib

import (
	"container/heap"
	"flag"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/mvm-sh/mvm/vm"
)

// PassthroughIface returns the underlying typed value of a mvm Iface,
// or a zero of its declared type when the Val is unset. Used as a
// vm.ProxyFactory for native functions that need the concrete value
// rather than a bridge wrapper (reflect.DeepEqual; errorsx targetProxy).
func PassthroughIface(_ *vm.Machine, ifc vm.Iface) reflect.Value {
	if rv := ifc.Val.Reflect(); rv.IsValid() {
		return rv
	}
	if ifc.Typ != nil {
		return reflect.Zero(ifc.Typ.Rtype)
	}
	return reflect.Value{}
}

// Bridge types for common interface methods.
// Each bridge is a struct with a Fn field and a pointer-receiver method
// that delegates to Fn. At the native call boundary, the VM allocates a
// bridge instance with Fn set to a closure that invokes the interpreted method.

// formatBridgeDisplay implements fmt.Formatter for display bridges.
// For %v/%s it writes the display string (from Error/String/GoString);
// for other verbs (%d, %x, etc.) it formats the concrete value directly,
// so named numeric types keep working with non-string format verbs.
func formatBridgeDisplay(f fmt.State, verb rune, display func() string, val any) {
	switch verb {
	case 'v', 's':
		_, _ = io.WriteString(f, display())
	default:
		_, _ = fmt.Fprintf(f, fmt.FormatString(f, verb), val)
	}
}

// BridgeError bridges the error interface method.
// Val holds the concrete value for non-string format verbs (%d, %x, etc.).
// FnFormat, when non-nil, dispatches to the interpreted type's own
// fmt.Formatter so user-defined Format(s, verb) bodies are invoked
// instead of the display fallback. Ifc preserves the original mvm
// Iface so the bridge can be unwrapped back at native->mvm boundaries
// (vm.UnbridgeIface).
type BridgeError struct {
	Fn       func() string
	FnFormat func(fmt.State, rune)
	Val      any
	Ifc      vm.Iface
}

// Error implements the error interface.
func (b *BridgeError) Error() string { return b.Fn() }

// Format implements fmt.Formatter. Routes to the user-defined Format
// method when set, otherwise uses the display fallback.
func (b *BridgeError) Format(f fmt.State, verb rune) {
	if b.FnFormat != nil {
		b.FnFormat(f, verb)
		return
	}
	formatBridgeDisplay(f, verb, b.Error, b.Val)
}

// Is enables stderrors.Is to compare two BridgeError instances that
// wrap the same underlying interpreted value. Two bridges of the same
// mvm Iface share Val (the interpreted struct pointer), so this gives
// stderrors.Is a value-level fallback when interface == fails.
func (b *BridgeError) Is(target error) bool {
	if t, ok := target.(bridgedValue); ok {
		return b.Val != nil && b.Val == t.bridgeVal()
	}
	return false
}

// bridgedValue exposes the underlying interpreted value held by a
// bridge, used for cross-bridge identity comparisons (Is, As).
type bridgedValue interface{ bridgeVal() any }

func (b *BridgeError) bridgeVal() any { return b.Val }

// BridgeGoString bridges the fmt.GoStringer interface method.
type BridgeGoString struct {
	Fn  func() string
	Val any
}

// GoString implements fmt.GoStringer.
func (b *BridgeGoString) GoString() string { return b.Fn() }

// Format implements fmt.Formatter.
func (b *BridgeGoString) Format(f fmt.State, verb rune) {
	formatBridgeDisplay(f, verb, b.GoString, b.Val)
}

// BridgeString bridges the fmt.Stringer interface method.
type BridgeString struct {
	Fn  func() string
	Val any
}

// String implements fmt.Stringer.
func (b *BridgeString) String() string { return b.Fn() }

// Format implements fmt.Formatter.
func (b *BridgeString) Format(f fmt.State, verb rune) {
	formatBridgeDisplay(f, verb, b.String, b.Val)
}

// BridgeFormat bridges the fmt.Formatter interface method.
// Used when an interpreted type defines its own Format(fmt.State, rune) so
// fmt routes every verb through user code rather than through the
// display-bridge fallback (which only handles %s/%v via Error/String/GoString).
type BridgeFormat struct {
	Fn func(fmt.State, rune)
}

// Format implements fmt.Formatter.
func (b *BridgeFormat) Format(f fmt.State, verb rune) { b.Fn(f, verb) }

// BridgeMarshalJSON bridges the json.Marshaler interface method.
type BridgeMarshalJSON struct{ Fn func() ([]byte, error) }

// MarshalJSON implements json.Marshaler.
func (b *BridgeMarshalJSON) MarshalJSON() ([]byte, error) { return b.Fn() }

// BridgeUnmarshalJSON bridges the json.Unmarshaler interface method.
type BridgeUnmarshalJSON struct{ Fn func([]byte) error }

// UnmarshalJSON implements json.Unmarshaler.
func (b *BridgeUnmarshalJSON) UnmarshalJSON(data []byte) error { return b.Fn(data) }

// BridgeWrite bridges the io.Writer interface method.
type BridgeWrite struct{ Fn func([]byte) (int, error) }

// Write implements io.Writer.
func (b *BridgeWrite) Write(p []byte) (int, error) { return b.Fn(p) }

// BridgeRead bridges the io.Reader interface method.
type BridgeRead struct{ Fn func([]byte) (int, error) }

// Read implements io.Reader.
func (b *BridgeRead) Read(p []byte) (int, error) { return b.Fn(p) }

// BridgeClose bridges the io.Closer interface method.
type BridgeClose struct{ Fn func() error }

// Close implements io.Closer.
func (b *BridgeClose) Close() error { return b.Fn() }

// BridgeWriteTo bridges the io.WriterTo interface method.
type BridgeWriteTo struct {
	Fn func(io.Writer) (int64, error)
}

// WriteTo implements io.WriterTo.
func (b *BridgeWriteTo) WriteTo(w io.Writer) (int64, error) { return b.Fn(w) }

// BridgeReadFrom bridges the io.ReaderFrom interface method.
type BridgeReadFrom struct {
	Fn func(io.Reader) (int64, error)
}

// ReadFrom implements io.ReaderFrom.
func (b *BridgeReadFrom) ReadFrom(r io.Reader) (int64, error) { return b.Fn(r) }

// BridgeReaderWriterTo is a composite bridge implementing io.Reader + io.WriterTo.
// Used to preserve WriterTo capability when wrapping for an io.Reader target (e.g. io.Copy).
type BridgeReaderWriterTo struct {
	FnRead    func([]byte) (int, error)
	FnWriteTo func(io.Writer) (int64, error)
}

func (b *BridgeReaderWriterTo) Read(p []byte) (int, error) { return b.FnRead(p) }

// WriteTo implements io.WriterTo.
func (b *BridgeReaderWriterTo) WriteTo(w io.Writer) (int64, error) { return b.FnWriteTo(w) }

// BridgeWriterReaderFrom is a composite bridge implementing io.Writer + io.ReaderFrom.
// Used to preserve ReaderFrom capability when wrapping for an io.Writer target (e.g. io.Copy).
type BridgeWriterReaderFrom struct {
	FnWrite    func([]byte) (int, error)
	FnReadFrom func(io.Reader) (int64, error)
}

func (b *BridgeWriterReaderFrom) Write(p []byte) (int, error) { return b.FnWrite(p) }

// ReadFrom implements io.ReaderFrom.
func (b *BridgeWriterReaderFrom) ReadFrom(r io.Reader) (int64, error) { return b.FnReadFrom(r) }

// BridgeSortInterface bridges sort.Interface (Len, Less, Swap).
type BridgeSortInterface struct {
	FnLen  func() int
	FnLess func(int, int) bool
	FnSwap func(int, int)
}

func (b *BridgeSortInterface) Len() int           { return b.FnLen() }
func (b *BridgeSortInterface) Less(i, j int) bool { return b.FnLess(i, j) }
func (b *BridgeSortInterface) Swap(i, j int)      { b.FnSwap(i, j) }

// BridgeHeapInterface bridges heap.Interface (Len, Less, Swap, Push, Pop).
// Embeds BridgeSortInterface for the sort.Interface methods.
type BridgeHeapInterface struct {
	BridgeSortInterface
	FnPush func(any)
	FnPop  func() any
}

// Push implements heap.Interface.
func (b *BridgeHeapInterface) Push(x any) { b.FnPush(x) }

// Pop implements heap.Interface.
func (b *BridgeHeapInterface) Pop() any { return b.FnPop() }

// BridgeUnwrap bridges the `interface{ Unwrap() error }` capability used
// by errors.Is / errors.As / errors.Unwrap chains.
type BridgeUnwrap struct{ Fn func() error }

// Unwrap implements the standard-library single-error unwrap protocol.
func (b *BridgeUnwrap) Unwrap() error { return b.Fn() }

// BridgeErrorUnwrap is the composite bridge for types that implement
// both Error() string and Unwrap() error. Required so a value bridged
// as `error` also satisfies the anonymous `interface{ Unwrap() error }`
// assertion that errors.Is / errors.As / errors.Unwrap perform when
// walking a chain. FnFormat is set (by wrapIfaceMulti) when the type
// also defines fmt.Formatter, so user Format() is invoked. Val holds
// the underlying interpreted value for identity-based comparisons in
// Is and for unbridgeValue.
type BridgeErrorUnwrap struct {
	FnError  func() string
	FnUnwrap func() error
	FnFormat func(fmt.State, rune)
	Val      any
	Ifc      vm.Iface
}

// Error implements the error interface.
func (b *BridgeErrorUnwrap) Error() string { return b.FnError() }

// Unwrap implements the standard-library single-error unwrap protocol.
func (b *BridgeErrorUnwrap) Unwrap() error { return b.FnUnwrap() }

// Format implements fmt.Formatter. Routes to the user-defined Format
// method when set, otherwise uses the display fallback.
func (b *BridgeErrorUnwrap) Format(f fmt.State, verb rune) {
	if b.FnFormat != nil {
		b.FnFormat(f, verb)
		return
	}
	formatBridgeDisplay(f, verb, b.Error, b.Val)
}

// Is enables stderrors.Is to match two BridgeErrorUnwrap instances
// that wrap the same underlying interpreted value.
func (b *BridgeErrorUnwrap) Is(target error) bool {
	if t, ok := target.(bridgedValue); ok {
		return b.Val != nil && b.Val == t.bridgeVal()
	}
	return false
}

func (b *BridgeErrorUnwrap) bridgeVal() any { return b.Val }

// BridgeFlagValue bridges flag.Value (String, Set).
type BridgeFlagValue struct {
	FnString func() string
	FnSet    func(string) error
}

// String implements flag.Value.
func (b *BridgeFlagValue) String() string { return b.FnString() }

// Set implements flag.Value.
func (b *BridgeFlagValue) Set(s string) error { return b.FnSet(s) }

func init() {
	vm.Bridges["Error"] = reflect.TypeOf((*BridgeError)(nil))
	vm.Bridges["Format"] = reflect.TypeOf((*BridgeFormat)(nil))
	vm.Bridges["GoString"] = reflect.TypeOf((*BridgeGoString)(nil))
	vm.Bridges["MarshalJSON"] = reflect.TypeOf((*BridgeMarshalJSON)(nil))
	vm.Bridges["String"] = reflect.TypeOf((*BridgeString)(nil))
	vm.Bridges["UnmarshalJSON"] = reflect.TypeOf((*BridgeUnmarshalJSON)(nil))
	vm.Bridges["Write"] = reflect.TypeOf((*BridgeWrite)(nil))
	vm.Bridges["Read"] = reflect.TypeOf((*BridgeRead)(nil))
	vm.Bridges["Close"] = reflect.TypeOf((*BridgeClose)(nil))
	vm.Bridges["WriteTo"] = reflect.TypeOf((*BridgeWriteTo)(nil))
	vm.Bridges["ReadFrom"] = reflect.TypeOf((*BridgeReadFrom)(nil))
	vm.Bridges["Unwrap"] = reflect.TypeOf((*BridgeUnwrap)(nil))

	vm.CompositeBridges[[2]string{"Read", "WriteTo"}] = reflect.TypeOf((*BridgeReaderWriterTo)(nil))
	vm.CompositeBridges[[2]string{"ReadFrom", "Write"}] = reflect.TypeOf((*BridgeWriterReaderFrom)(nil))
	// Sorted alphabetically: Error < Unwrap.
	vm.CompositeBridges[[2]string{"Error", "Unwrap"}] = reflect.TypeOf((*BridgeErrorUnwrap)(nil))

	// Display bridges are used when the target is interface{}/any.
	// MarshalJSON/UnmarshalJSON are deliberately omitted: they are not
	// display methods, and fmt never calls them. JSON encoding of
	// interpreted values is routed through stdlib/jsonx arg proxies.
	vm.DisplayBridges["Error"] = true
	vm.DisplayBridges["Format"] = true
	vm.DisplayBridges["GoString"] = true
	vm.DisplayBridges["String"] = true

	vm.ValBridgeTypes[reflect.TypeOf((*BridgeError)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorUnwrap)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeGoString)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeString)(nil))] = true

	vm.InterfaceBridges[reflect.TypeOf((*sort.Interface)(nil)).Elem()] = reflect.TypeOf((*BridgeSortInterface)(nil))
	vm.InterfaceBridges[reflect.TypeOf((*heap.Interface)(nil)).Elem()] = reflect.TypeOf((*BridgeHeapInterface)(nil))
	vm.InterfaceBridges[reflect.TypeOf((*flag.Value)(nil)).Elem()] = reflect.TypeOf((*BridgeFlagValue)(nil))

	vm.RegisterArgProxy(reflect.DeepEqual, 0, PassthroughIface)
	vm.RegisterArgProxy(reflect.DeepEqual, 1, PassthroughIface)
}
