package vm

import "github.com/mvm-sh/mvm/mtype"

// The symbolic Type layer now lives in package mtype. The aliases below are
// transitional: they keep existing vm.Type / vm.SliceOf / ... call sites
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
// construction, synth cascade, and patch helpers (PointerTo, SliceOf, MapOf,
// ArrayOf, ChanOf, CanonicalType, LiveFieldRtype, PatchSynth*, RefreshRtype,
// ...) are runtime materialization and live in vm (derive.go), not mtype.
var (
	TypeOf        = mtype.TypeOf
	FuncOf        = mtype.FuncOf
	StructOf      = mtype.StructOf
	NewStructType = mtype.NewStructType
)

var ifaceMethodTypes = mtype.IfaceMethodTypes
