package stdlib

import "github.com/mvm-sh/mvm/goparser"

// atomicGenericShim defines atomic.Pointer[T], which is generic and so can't be reflect-bridged.
// It is built on the bridged {Load,Store,Swap,CompareAndSwap}Pointer functions, matching upstream.
const atomicGenericShim = `package atomic

import "unsafe"

type Pointer[T any] struct {
	v unsafe.Pointer
}

func (x *Pointer[T]) Load() *T         { return (*T)(LoadPointer(&x.v)) }
func (x *Pointer[T]) Store(val *T)     { StorePointer(&x.v, unsafe.Pointer(val)) }
func (x *Pointer[T]) Swap(new *T) *T   { return (*T)(SwapPointer(&x.v, unsafe.Pointer(new))) }
func (x *Pointer[T]) CompareAndSwap(old, new *T) bool {
	return CompareAndSwapPointer(&x.v, unsafe.Pointer(old), unsafe.Pointer(new))
}
`

func init() {
	goparser.RegisterGenericShim("sync/atomic", atomicGenericShim,
		[]string{"LoadPointer", "StorePointer", "SwapPointer", "CompareAndSwapPointer"})
}
