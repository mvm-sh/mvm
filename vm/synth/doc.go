// Package synth synthesizes Go rtypes with interpreted-method metadata.
// Native code can then invoke methods on interpreter objects directly.
//
// The mirrors here track internal/abi.Type, StructType, UncommonType, and
// Method byte-for-byte.
// Layout drift is caught by abi_test.go probes against a real native rtype.
package synth
