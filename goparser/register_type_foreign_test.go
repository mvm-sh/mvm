package goparser

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// registerType must key a composite-literal type of a FOREIGN package under that
// type's own identity, not under the package currently being compiled. Mirrors
// `mvm test proto`: proto declares `type Message = protoreflect.ProtoMessage`
// (an interface alias) and also builds `protobuild.Message{...}` (a different
// package's map type, same short name). registerType qualified ANY named type
// under the compiling package (proto.Message), so the foreign composite
// overwrote the alias and a later `v.ProtoReflect()` resolved its receiver to
// the map type, failing at compile time with "undefined: ProtoReflect".
func TestRegisterTypeForeignDoesNotClobberAlias(t *testing.T) {
	p := NewParser(golang.GoSpec, false)

	// Package pp (the one being compiled) declares `type Message = <iface>`; its
	// canonical symbol lives at "pp.Message".
	alias := &vm.Type{Name: "Message", PkgPath: "pp", Rtype: reflect.TypeOf((*any)(nil)).Elem()}
	aliasSym := &symbol.Symbol{Kind: symbol.Type, Name: "pp.Message", Type: alias}
	p.Symbols["pp.Message"] = aliasSym
	p.CompilingPkg = "pp"

	// A composite literal of a FOREIGN package's same-named type.
	foreign := &vm.Type{Name: "Message", PkgPath: "pb", Rtype: reflect.TypeOf(map[string]int(nil))}
	var out Tokens
	if key := p.registerType(foreign, 0, &out); key == "pp.Message" {
		t.Fatalf("foreign type keyed under compiling pkg as %q, clobbering the alias", key)
	}
	if got := p.Symbols["pp.Message"]; got != aliasSym || got.Type != alias {
		t.Fatal("alias pp.Message was overwritten by the foreign composite type")
	}

	// A LOCAL type of the compiling package must still qualify under its canonical
	// pkg key so a sibling import's bare-key write cannot shadow it.
	local := &vm.Type{Name: "Local", PkgPath: "pp", Rtype: reflect.TypeOf(struct{}{})}
	var out2 Tokens
	if key := p.registerType(local, 0, &out2); key != "pp.Local" {
		t.Fatalf("local type keyed as %q, want pp.Local", key)
	}
}
