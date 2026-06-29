// Package runtype synthesizes Go rtypes carrying interpreted-method metadata.
// Native code can then invoke methods on interpreter objects directly.
//
// The mirrors here track internal/abi.Type, StructType, UncommonType, and
// Method byte-for-byte.
// Layout drift is caught by abi_test.go probes against a real native rtype.
//
// runtype carries no method-signature/handler knowledge: Attach* take resolved
// stub PCs (MethodSpec).
// The shape pools producing those PCs live in stdlib/stubs, which imports runtype
// one-directionally.
package runtype
