package vm

import "github.com/mvm-sh/mvm/mtype"

// The symbolic Type layer now lives in package mtype. The aliases below are
// transitional: they keep existing vm.Type / vm.TypeOf / ... call sites
// compiling while callers migrate to reference mtype directly.

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

// Symbolic type constructors re-exported from mtype. The derived-type
// constructors and reserve/fill helpers (SymPtr/SymSlice/..., PointerTo,
// CanonicalType, AttachPtrDerived, ...) are runtime materialization and live in
// vm (derive.go), not mtype.
var (
	TypeOf        = mtype.TypeOf
	FuncOf        = mtype.FuncOf
	StructOf      = mtype.StructOf
	NewStructType = mtype.NewStructType
)

// Symbolic (Rtype-nil) leaf/aggregate constructors goparser uses post-flip; comp
// materializes the rtype later (see MaterializeRtype). The derived constructors
// (SymPtr/SymSlice/SymArray/SymChan/SymMap) live in derive.go: they memoize and
// register the derived *Type in the cache, leaving Rtype nil for comp to fill.
var (
	SymFunc   = mtype.SymFunc
	SymStruct = mtype.SymStruct
	SymBasic  = mtype.SymBasic
)

var ifaceMethodTypes = mtype.IfaceMethodTypes
