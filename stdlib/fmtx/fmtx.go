// Package fmtx patches fmt so that %T over an interpreted value prints its
// source-level type name (e.g. errors_test.MyAsError) instead of the display
// bridge wrapper (*stdlib.BridgeError).
package fmtx

import (
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/vm"
)

func init() {
	stdlib.RegisterPackagePatcher("fmt", patchFmt)
}

func patchFmt(m *vm.Machine, values map[string]vm.Value) {
	values["Errorf"] = vm.FromReflect(reflect.ValueOf(func(format string, args ...any) error {
		f, a := rewriteTypeVerbs(format, args)
		return fmt.Errorf(f, a...)
	}))
	values["Printf"] = vm.FromReflect(reflect.ValueOf(func(format string, args ...any) (int, error) {
		f, a := rewriteTypeVerbs(format, args)
		return fmt.Fprintf(m.Out(), f, a...)
	}))
	values["Sprintf"] = vm.FromReflect(reflect.ValueOf(func(format string, args ...any) string {
		f, a := rewriteTypeVerbs(format, args)
		return fmt.Sprintf(f, a...)
	}))
	values["Fprintf"] = vm.FromReflect(reflect.ValueOf(func(w io.Writer, format string, args ...any) (int, error) {
		f, a := rewriteTypeVerbs(format, args)
		return fmt.Fprintf(w, f, a...)
	}))
	values["Appendf"] = vm.FromReflect(reflect.ValueOf(func(b []byte, format string, args ...any) []byte {
		f, a := rewriteTypeVerbs(format, args)
		return fmt.Appendf(b, f, a...)
	}))
}

func rewriteTypeVerbs(format string, args []any) (string, []any) {
	if !containsTypeVerb(format) {
		return format, args
	}
	var sb strings.Builder
	sb.Grow(len(format) + 16)
	out := make([]any, 0, len(args))
	argIdx := 0
	i := 0
	for i < len(format) {
		if format[i] != '%' {
			sb.WriteByte(format[i])
			i++
			continue
		}
		j := i + 1
		for j < len(format) && isVerbModifier(format[j]) {
			j++
		}
		if j >= len(format) {
			sb.WriteString(format[i:])
			break
		}
		verb := format[j]
		if verb == '%' {
			sb.WriteString(format[i : j+1])
			i = j + 1
			continue
		}
		if verb == 'T' && argIdx < len(args) && !strings.ContainsAny(format[i+1:j], "[*") {
			sb.WriteString(format[i:j])
			sb.WriteByte('s')
			out = append(out, mvmTypeName(args[argIdx]))
		} else {
			sb.WriteString(format[i : j+1])
			if argIdx < len(args) {
				out = append(out, args[argIdx])
			}
		}
		argIdx++
		i = j + 1
	}
	for ; argIdx < len(args); argIdx++ {
		out = append(out, args[argIdx])
	}
	return sb.String(), out
}

func isVerbModifier(b byte) bool {
	switch b {
	case '+', '-', ' ', '#', '0', '.', '*', '[', ']',
		'1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return false
}

func containsTypeVerb(format string) bool {
	i := 0
	for i < len(format) {
		if format[i] != '%' {
			i++
			continue
		}
		j := i + 1
		for j < len(format) && isVerbModifier(format[j]) {
			j++
		}
		if j < len(format) && format[j] == 'T' {
			return true
		}
		i = j + 1
	}
	return false
}

func mvmTypeName(v any) string {
	if v != nil {
		if ifc, ok := vm.UnbridgeIface(reflect.ValueOf(v)); ok && ifc.Typ != nil && ifc.Typ.Name != "" {
			if ifc.Typ.PkgPath != "" {
				return ifc.Typ.PkgPath + "." + ifc.Typ.Name
			}
			return ifc.Typ.Name
		}
	}
	return fmt.Sprintf("%T", v)
}
