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
		return bridgeValEqual(b.Val, t.bridgeVal())
	}
	return false
}

// bridgedValue exposes the underlying interpreted value held by a bridge.
type bridgedValue interface{ bridgeVal() any }

func (b *BridgeError) bridgeVal() any { return b.Val }

func bridgeValEqual(a, b any) bool {
	if a == nil || b == nil {
		return false
	}
	ta := reflect.TypeOf(a)
	if ta != reflect.TypeOf(b) || !ta.Comparable() {
		return false
	}
	return a == b
}

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

// errBridgeBase carries the fields and methods shared by every composite
// error bridge below. Each composite embeds it and adds only its
// distinguishing Fn* fields plus the interface methods (Is, As, Unwrap)
// that define its method set. The field names (FnError, FnFormat, Val,
// Ifc) are load-bearing: vm.wrapIfaceMulti wires closures via
// FieldByName("Fn"+method), and populateBridgeAux / UnbridgeValue /
// UnbridgeIface set Val/Ifc/FnFormat -- all resolve through promotion
// from the embedded base (exported fields stay settable even though the
// base type is unexported).
type errBridgeBase struct {
	FnError  func() string
	FnFormat func(fmt.State, rune)
	Val      any
	Ifc      vm.Iface
}

// Error implements the error interface.
func (b *errBridgeBase) Error() string { return b.FnError() }

// Format implements fmt.Formatter, routing to the user-defined Format
// method when set, otherwise the display fallback.
func (b *errBridgeBase) Format(f fmt.State, verb rune) {
	if b.FnFormat != nil {
		b.FnFormat(f, verb)
		return
	}
	formatBridgeDisplay(f, verb, b.FnError, b.Val)
}

func (b *errBridgeBase) bridgeVal() any { return b.Val }

// identityIs reports whether target is a bridge wrapping the same
// underlying interpreted value. Used by the single-error Is bridges as a
// cross-bridge identity fallback; the *UnwrapMulti bridges skip it because
// their Val is an uncomparable []error.
func (b *errBridgeBase) identityIs(target error) bool {
	if t, ok := target.(bridgedValue); ok {
		return bridgeValEqual(b.Val, t.bridgeVal())
	}
	return false
}

// BridgeErrorUnwrap is the composite bridge for Error + Unwrap() error.
type BridgeErrorUnwrap struct {
	errBridgeBase
	FnUnwrap func() error
}

// Unwrap implements the standard-library single-error unwrap protocol.
func (b *BridgeErrorUnwrap) Unwrap() error { return b.FnUnwrap() }

// Is matches two bridges wrapping the same underlying interpreted value.
func (b *BridgeErrorUnwrap) Is(target error) bool { return b.identityIs(target) }

// BridgeIs and BridgeAs exist so that an Is or As method gets counted
// as bridgeable when wrapIface selects a multi-method composite.
type BridgeIs struct{ Fn func(error) bool }

// Is implements interface{ Is(error) bool }.
func (b *BridgeIs) Is(target error) bool { return b.Fn(target) }

// BridgeAs - see BridgeIs.
type BridgeAs struct{ Fn func(any) bool }

// As implements interface{ As(any) bool }.
func (b *BridgeAs) As(target any) bool { return b.Fn(target) }

// BridgeErrorIsAsUnwrap is the composite bridge for Error + Is + As + Unwrap() error.
type BridgeErrorIsAsUnwrap struct {
	errBridgeBase
	FnIs     func(error) bool
	FnAs     func(any) bool
	FnUnwrap func() error
}

// Unwrap implements interface{ Unwrap() error }.
func (b *BridgeErrorIsAsUnwrap) Unwrap() error { return b.FnUnwrap() }

// Is short-circuits on cross-bridge identity, otherwise delegates to the
// interpreted Is body.
func (b *BridgeErrorIsAsUnwrap) Is(target error) bool {
	if b.identityIs(target) {
		return true
	}
	return b.FnIs(target)
}

// As delegates to the interpreted As body.
func (b *BridgeErrorIsAsUnwrap) As(target any) bool { return b.FnAs(target) }

// BridgeErrorIs is the composite bridge for Error + Is(error) bool.
type BridgeErrorIs struct {
	errBridgeBase
	FnIs func(error) bool
}

// Is short-circuits on cross-bridge identity, otherwise delegates to the
// interpreted Is body.
func (b *BridgeErrorIs) Is(target error) bool {
	if b.identityIs(target) {
		return true
	}
	return b.FnIs(target)
}

// BridgeErrorAs is the composite bridge for Error + As(any) bool (no Unwrap/Is).
type BridgeErrorAs struct {
	errBridgeBase
	FnAs func(any) bool
}

// As delegates to the interpreted As body.
func (b *BridgeErrorAs) As(target any) bool { return b.FnAs(target) }

// BridgeErrorIsAs is the composite bridge for Error + Is + As (no Unwrap).
type BridgeErrorIsAs struct {
	errBridgeBase
	FnIs func(error) bool
	FnAs func(any) bool
}

// Is short-circuits on cross-bridge identity, otherwise delegates to the
// interpreted Is body.
func (b *BridgeErrorIsAs) Is(target error) bool {
	if b.identityIs(target) {
		return true
	}
	return b.FnIs(target)
}

// As delegates to the interpreted As body.
func (b *BridgeErrorIsAs) As(target any) bool { return b.FnAs(target) }

// BridgeErrorIsUnwrap is the composite bridge for Error + Is + Unwrap() error.
// Resolves this 3-method set deterministically so the chain walk reaches both
// the interpreted Is body and the Unwrap link (the pair-only composites would
// keep only one).
type BridgeErrorIsUnwrap struct {
	errBridgeBase
	FnIs     func(error) bool
	FnUnwrap func() error
}

// Unwrap implements the standard-library single-error unwrap protocol.
func (b *BridgeErrorIsUnwrap) Unwrap() error { return b.FnUnwrap() }

// Is short-circuits on cross-bridge identity, otherwise delegates to the
// interpreted Is body.
func (b *BridgeErrorIsUnwrap) Is(target error) bool {
	if b.identityIs(target) {
		return true
	}
	return b.FnIs(target)
}

// BridgeErrorAsUnwrap is the composite bridge for Error + As + Unwrap() error.
// Resolves this 3-method set deterministically so the chain walk reaches both
// the interpreted As body and the Unwrap link (the pair-only composites would
// keep only one).
type BridgeErrorAsUnwrap struct {
	errBridgeBase
	FnAs     func(any) bool
	FnUnwrap func() error
}

// Unwrap implements the standard-library single-error unwrap protocol.
func (b *BridgeErrorAsUnwrap) Unwrap() error { return b.FnUnwrap() }

// As delegates to the interpreted As body.
func (b *BridgeErrorAsUnwrap) As(target any) bool { return b.FnAs(target) }

// The *UnwrapMulti bridges mirror the single-error Unwrap bridges above but
// expose Unwrap() []error (the multierror protocol from errors.Join and
// hashicorp-style trees). vm.bridgeMethodName remaps an interpreted
// Unwrap() []error to the synthetic "UnwrapMulti" bridge key so it is
// selected here instead of colliding with the single-error Unwrap() error
// bridge (whose func() error field cannot hold a []error-returning method).

// BridgeUnwrapMulti bridges the `interface{ Unwrap() []error }` capability.
type BridgeUnwrapMulti struct{ Fn func() []error }

// Unwrap implements the standard-library multi-error unwrap protocol.
func (b *BridgeUnwrapMulti) Unwrap() []error { return b.Fn() }

// BridgeErrorUnwrapMulti is the composite bridge for Error + Unwrap() []error.
// It has no identity Is method (unlike single-error BridgeErrorUnwrap): a
// multierror's underlying Val is typically an uncomparable []error, so the
// identity comparison would panic. This also matches errors.Is, which skips
// the err == target identity check for uncomparable errors.
type BridgeErrorUnwrapMulti struct {
	errBridgeBase
	FnUnwrapMulti func() []error
}

// Unwrap implements the standard-library multi-error unwrap protocol.
func (b *BridgeErrorUnwrapMulti) Unwrap() []error { return b.FnUnwrapMulti() }

// BridgeErrorIsUnwrapMulti is the composite bridge for Error + Is + Unwrap() []error.
type BridgeErrorIsUnwrapMulti struct {
	errBridgeBase
	FnIs          func(error) bool
	FnUnwrapMulti func() []error
}

// Unwrap implements the standard-library multi-error unwrap protocol.
func (b *BridgeErrorIsUnwrapMulti) Unwrap() []error { return b.FnUnwrapMulti() }

// Is delegates to the interpreted Is body. Unlike the single-error Is
// bridges it has no cross-bridge identity short-circuit: a multierror's
// Val is typically an uncomparable []error, so b.Val == other would panic
// (and errors.Is itself skips identity for uncomparable errors).
func (b *BridgeErrorIsUnwrapMulti) Is(target error) bool { return b.FnIs(target) }

// BridgeErrorAsUnwrapMulti is the composite bridge for Error + As + Unwrap() []error.
type BridgeErrorAsUnwrapMulti struct {
	errBridgeBase
	FnAs          func(any) bool
	FnUnwrapMulti func() []error
}

// Unwrap implements the standard-library multi-error unwrap protocol.
func (b *BridgeErrorAsUnwrapMulti) Unwrap() []error { return b.FnUnwrapMulti() }

// As delegates to the interpreted As body.
func (b *BridgeErrorAsUnwrapMulti) As(target any) bool { return b.FnAs(target) }

// BridgeErrorIsAsUnwrapMulti is the composite bridge for Error + Is + As +
// Unwrap() []error (the multierror analog of BridgeErrorIsAsUnwrap).
type BridgeErrorIsAsUnwrapMulti struct {
	errBridgeBase
	FnIs          func(error) bool
	FnAs          func(any) bool
	FnUnwrapMulti func() []error
}

// Unwrap implements the standard-library multi-error unwrap protocol.
func (b *BridgeErrorIsAsUnwrapMulti) Unwrap() []error { return b.FnUnwrapMulti() }

// Is delegates to the interpreted Is body; no identity short-circuit (see
// BridgeErrorIsUnwrapMulti.Is for why multierror Val can't be compared).
func (b *BridgeErrorIsAsUnwrapMulti) Is(target error) bool { return b.FnIs(target) }

// As delegates to the interpreted As body.
func (b *BridgeErrorIsAsUnwrapMulti) As(target any) bool { return b.FnAs(target) }

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
	// "UnwrapMulti" is a synthetic name (no interpreted method is literally
	// called that); vm.bridgeMethodName maps Unwrap() []error to it.
	vm.Bridges["UnwrapMulti"] = reflect.TypeOf((*BridgeUnwrapMulti)(nil))
	vm.Bridges["Is"] = reflect.TypeOf((*BridgeIs)(nil))
	vm.Bridges["As"] = reflect.TypeOf((*BridgeAs)(nil))

	vm.CompositeBridges[[2]string{"Read", "WriteTo"}] = reflect.TypeOf((*BridgeReaderWriterTo)(nil))
	vm.CompositeBridges[[2]string{"ReadFrom", "Write"}] = reflect.TypeOf((*BridgeWriterReaderFrom)(nil))
	// Sorted alphabetically: Error < Unwrap.
	vm.CompositeBridges[[2]string{"Error", "Unwrap"}] = reflect.TypeOf((*BridgeErrorUnwrap)(nil))
	// Error+Is / Error+As, so errors.Is / errors.As reach the interpreted
	// Is/As body instead of the single-method BridgeError fallback.
	vm.CompositeBridges[[2]string{"Error", "Is"}] = reflect.TypeOf((*BridgeErrorIs)(nil))
	vm.CompositeBridges[[2]string{"As", "Error"}] = reflect.TypeOf((*BridgeErrorAs)(nil))
	// Error + Unwrap() []error (multierror), keyed on the synthetic "UnwrapMulti".
	vm.CompositeBridges[[2]string{"Error", "UnwrapMulti"}] = reflect.TypeOf((*BridgeErrorUnwrapMulti)(nil))

	vm.RegisterMultiCompositeBridge(vm.MultiCompositeBridge{
		Methods: []string{"As", "Error", "Is", "Unwrap"}, // alphabetical
		Type:    reflect.TypeOf((*BridgeErrorIsAsUnwrap)(nil)),
	})
	// 3-method combos, resolved by the multi-composite block before the
	// (order-dependent) pair loop so neither Is/As nor Unwrap is dropped.
	vm.RegisterMultiCompositeBridge(vm.MultiCompositeBridge{
		Methods: []string{"As", "Error", "Is"}, // alphabetical
		Type:    reflect.TypeOf((*BridgeErrorIsAs)(nil)),
	})
	vm.RegisterMultiCompositeBridge(vm.MultiCompositeBridge{
		Methods: []string{"Error", "Is", "Unwrap"}, // alphabetical
		Type:    reflect.TypeOf((*BridgeErrorIsUnwrap)(nil)),
	})
	vm.RegisterMultiCompositeBridge(vm.MultiCompositeBridge{
		Methods: []string{"As", "Error", "Unwrap"}, // alphabetical
		Type:    reflect.TypeOf((*BridgeErrorAsUnwrap)(nil)),
	})
	// multierror (Unwrap() []error) combos with Is/As, keyed on "UnwrapMulti".
	vm.RegisterMultiCompositeBridge(vm.MultiCompositeBridge{
		Methods: []string{"Error", "Is", "UnwrapMulti"}, // alphabetical
		Type:    reflect.TypeOf((*BridgeErrorIsUnwrapMulti)(nil)),
	})
	vm.RegisterMultiCompositeBridge(vm.MultiCompositeBridge{
		Methods: []string{"As", "Error", "UnwrapMulti"}, // alphabetical
		Type:    reflect.TypeOf((*BridgeErrorAsUnwrapMulti)(nil)),
	})
	vm.RegisterMultiCompositeBridge(vm.MultiCompositeBridge{
		Methods: []string{"As", "Error", "Is", "UnwrapMulti"}, // alphabetical
		Type:    reflect.TypeOf((*BridgeErrorIsAsUnwrapMulti)(nil)),
	})

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
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorIsAsUnwrap)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorIs)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorAs)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorIsAs)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorIsUnwrap)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorAsUnwrap)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorUnwrapMulti)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorIsUnwrapMulti)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorAsUnwrapMulti)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeErrorIsAsUnwrapMulti)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeGoString)(nil))] = true
	vm.ValBridgeTypes[reflect.TypeOf((*BridgeString)(nil))] = true

	vm.InterfaceBridges[reflect.TypeOf((*sort.Interface)(nil)).Elem()] = reflect.TypeOf((*BridgeSortInterface)(nil))
	vm.InterfaceBridges[reflect.TypeOf((*heap.Interface)(nil)).Elem()] = reflect.TypeOf((*BridgeHeapInterface)(nil))
	vm.InterfaceBridges[reflect.TypeOf((*flag.Value)(nil)).Elem()] = reflect.TypeOf((*BridgeFlagValue)(nil))

	vm.RegisterArgProxy(reflect.DeepEqual, 0, PassthroughIface)
	vm.RegisterArgProxy(reflect.DeepEqual, 1, PassthroughIface)

	// sort.Slice* take the slice as `any` and drive it through reflect.Swapper /
	// reflect.ValueOf, so the raw slice must reach them unwrapped. Without these
	// the any-arg display bridging (which wraps a slice whose element type
	// defines String/Error/Format/GoString into a fmt wrapper) would hand sort a
	// *wrapper pointer, panicking with "reflect: call of Swapper on ptr Value".
	vm.RegisterArgProxy(sort.Slice, 0, PassthroughIface)
	vm.RegisterArgProxy(sort.SliceStable, 0, PassthroughIface)
	vm.RegisterArgProxy(sort.SliceIsSorted, 0, PassthroughIface)
}
