package synth

import "unsafe"

// abiType mirrors internal/abi.Type.
// Verified identical Go 1.24.13 through 1.26.3.
// Field order and types must match runtime layout.
type abiType struct {
	Size       uintptr
	PtrBytes   uintptr
	Hash       uint32
	TFlag      uint8
	Align      uint8
	FieldAlign uint8
	Kind       uint8
	Equal      func(unsafe.Pointer, unsafe.Pointer) bool
	GCData     *byte
	Str        int32 // NameOff
	PtrToThis  int32 // TypeOff
}

// TFlag bits, mirroring internal/abi.TFlag.
const (
	tflagUncommon       uint8 = 1 << 0
	tflagExtraStar      uint8 = 1 << 1
	tflagNamed          uint8 = 1 << 2
	tflagRegularMemory  uint8 = 1 << 3
	tflagGCMaskOnDemand uint8 = 1 << 4
	tflagDirectIface    uint8 = 1 << 5
)

// Kind values, mirroring internal/abi.Kind iota positions.
// Values are stable since 1.18.
const (
	kindInvalid       uint8 = 0
	kindBool          uint8 = 1
	kindInt           uint8 = 2
	kindUint          uint8 = 7
	kindFloat32       uint8 = 13
	kindFloat64       uint8 = 14
	kindComplex64     uint8 = 15
	kindComplex128    uint8 = 16
	kindArray         uint8 = 17
	kindChan          uint8 = 18
	kindFunc          uint8 = 19
	kindInterface     uint8 = 20
	kindMap           uint8 = 21
	kindPointer       uint8 = 22
	kindSlice         uint8 = 23
	kindString        uint8 = 24
	kindStruct        uint8 = 25
	kindUnsafePointer uint8 = 26
)

// abiName mirrors internal/abi.Name.
type abiName struct {
	Bytes *byte
}

// abiStructField mirrors internal/abi.StructField.
type abiStructField struct {
	Name   abiName
	Typ    *abiType
	Offset uintptr
}

// abiStructType mirrors internal/abi.StructType.
type abiStructType struct {
	abiType
	PkgPath abiName
	Fields  []abiStructField
}

// abiPtrType mirrors internal/abi.PtrType.
type abiPtrType struct {
	abiType
	Elem *abiType
}

// abiSliceType mirrors internal/abi.SliceType.
type abiSliceType struct {
	abiType
	Elem *abiType
}

// abiArrayType mirrors internal/abi.ArrayType.
type abiArrayType struct {
	abiType
	Elem  *abiType
	Slice *abiType
	Len   uintptr
}

// abiMapType mirrors internal/abi.MapType.
type abiMapType struct {
	abiType
	Key       *abiType
	Elem      *abiType
	Group     *abiType
	Hasher    func(unsafe.Pointer, uintptr) uintptr
	GroupSize uintptr
	SlotSize  uintptr
	ElemOff   uintptr
	Flags     uint32
	_         uint32 // align following UncommonType on 32 and 64-bit targets
}

// abiUncommon mirrors internal/abi.UncommonType.
type abiUncommon struct {
	PkgPath uint32 // NameOff
	Mcount  uint16
	Xcount  uint16
	Moff    uint32 // offset from this struct to the [Mcount]Method array
	_       uint32 // unused
}

// abiMethod mirrors internal/abi.Method.
type abiMethod struct {
	Name uint32 // NameOff
	Mtyp uint32 // TypeOff
	Ifn  uint32 // TextOff (interface-dispatch entry)
	Tfn  uint32 // TextOff (direct method-value entry)
}
