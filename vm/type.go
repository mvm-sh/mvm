package vm

import (
	"github.com/mvm-sh/mvm/derive"
	"github.com/mvm-sh/mvm/mtype"
)

// Type lives in mtype, the derivation/materialization layer in derive.
// These aliases are transitional: they keep vm.Type / vm.SymPtr / ... callers
// compiling until they migrate to mtype/derive directly.

// Type is the symbolic type representation, re-exported from mtype.
type Type = mtype.Type

// Method is re-exported from mtype.
type Method = mtype.Method

// EmbeddedField is re-exported from mtype.
type EmbeddedField = mtype.EmbeddedField

// IfaceMethod is re-exported from mtype.
type IfaceMethod = mtype.IfaceMethod

// TypeElem is re-exported from mtype.
type TypeElem = mtype.TypeElem

// AnyRtype is the empty-interface rtype, re-exported from mtype.
var AnyRtype = mtype.AnyRtype

// Symbolic type constructors re-exported from mtype.
var (
	TypeOf        = mtype.TypeOf
	FuncOf        = mtype.FuncOf
	StructOf      = mtype.StructOf
	NewStructType = mtype.NewStructType
)

// Symbolic (Rtype-nil) leaf/aggregate constructors goparser uses post-flip; comp
// materializes the rtype later (see MaterializeRtype).
var (
	SymFunc   = mtype.SymFunc
	SymStruct = mtype.SymStruct
	SymBasic  = mtype.SymBasic
)

// Derived-type construction and materialization, re-exported from derive.
// Unlike mtype's primitive Sym*, these memoize the derived *Type.
var (
	SymPtr                 = derive.SymPtr
	SymSlice               = derive.SymSlice
	SymArray               = derive.SymArray
	SymChan                = derive.SymChan
	SymMap                 = derive.SymMap
	PointerTo              = derive.PointerTo
	CanonicalType          = derive.CanonicalType
	AttachPtrDerived       = derive.AttachPtrDerived
	MaterializeRtype       = derive.MaterializeRtype
	MaterializeMethod      = derive.MaterializeMethod
	MaterializeIfaceMethod = derive.MaterializeIfaceMethod
	FinalizeDeferred       = derive.FinalizeDeferred
	RegisterNativeLayout   = derive.RegisterNativeLayout
)

var ifaceMethodTypes = mtype.IfaceMethodTypes
