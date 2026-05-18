package vm

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/scan"
)

// DebugInfo holds symbolic information for annotating debug output.
// Built by the compiler from the symbol table and source registry.
type DebugInfo struct {
	Sources scan.Sources          // source position registry (multi-file / REPL)
	Labels  map[int]string        // code address -> label/function name
	Funcs   []FuncRange           // function bytecode ranges, used by FuncAt to pick the innermost frame
	Globals map[int]string        // data index -> symbol name
	Locals  map[string][]LocalVar // function name -> local variable list
}

// FuncRange describes the bytecode range of a function (or anonymous
// closure). End is one past the last instruction of the body (i.e. an
// exclusive bound). Closures are emitted inline within their outer
// function so ranges nest; FuncAt picks the innermost containing range.
type FuncRange struct {
	Start int
	End   int
	Name  string
}

// LocalVar describes a local variable within a function frame.
type LocalVar struct {
	Offset int    // offset from fp (1-based, as in Get Local N)
	Name   string // variable name (short, without scope prefix)
}

// NewDebugInfo returns an empty DebugInfo ready to be populated.
func NewDebugInfo() *DebugInfo {
	return &DebugInfo{
		Labels:  map[int]string{},
		Globals: map[int]string{},
		Locals:  map[string][]LocalVar{},
	}
}

// FuncAt returns the name of the innermost function whose bytecode
// range contains ip. Falls back to a "largest start <= ip" scan over
// Labels when Funcs is empty (e.g. snapshots produced before
// BuildDebugInfo started populating ranges). Returns "" when no
// function qualifies.
func (d *DebugInfo) FuncAt(ip int) string {
	if d == nil {
		return ""
	}
	if len(d.Funcs) > 0 {
		bestLen := 0
		bestName := ""
		for _, fr := range d.Funcs {
			if ip < fr.Start || ip >= fr.End {
				continue
			}
			length := fr.End - fr.Start
			if bestName == "" || length < bestLen {
				bestLen = length
				bestName = fr.Name
			}
		}
		if bestName != "" {
			return bestName
		}
	}
	bestAddr := -1
	bestName := ""
	for addr, name := range d.Labels {
		if addr <= ip && addr > bestAddr {
			bestAddr = addr
			bestName = name
		}
	}
	return bestName
}

// PosToLine converts a global byte offset to a human-readable location string.
// Returns "" if no sources are registered or pos is out of range.
func (d *DebugInfo) PosToLine(pos Pos) string {
	if d == nil || len(d.Sources) == 0 {
		return ""
	}
	return d.Sources.FormatPos(int(pos))
}

// LocalName returns the variable name for a local slot offset within func funcName.
func (d *DebugInfo) LocalName(funcName string, offset int) string {
	if d == nil {
		return ""
	}
	for _, lv := range d.Locals[funcName] {
		if lv.Offset == offset {
			return lv.Name
		}
	}
	return ""
}

// DumpFrame decodes and pretty-prints the call frame at the given fp.
// di is optional (may be nil); when set, slots are annotated with variable names.
func DumpFrame(w io.Writer, mem []Value, code Code, fp, sp, narg, nret int, di *DebugInfo) {
	if fp < frameOverhead || fp > len(mem) {
		_, _ = fmt.Fprintf(w, "--- invalid fp=%d (mem len=%d) ---\n", fp, len(mem))
		return
	}

	retIP := int(mem[fp-2].num)  //nolint:gosec
	prevFP := int(mem[fp-1].num) //nolint:gosec

	funcAddr := max(fp-frameOverhead-narg-1, 0)

	// Resolve function name from the code address stored in the func slot.
	funcName := ""
	if di != nil {
		codeAddr := 0
		fv := mem[funcAddr]
		if isNum(fv.ref.Kind()) {
			codeAddr = int(fv.num) //nolint:gosec
		} else if fv.ref.IsValid() {
			if iv, ok := fv.ref.Interface().(int); ok {
				codeAddr = iv
			}
		}
		funcName = di.Labels[codeAddr]
	}

	// Header line.
	header := fmt.Sprintf("--- Frame fp=%d retIP=%d prevFP=%d narg=%d nret=%d", fp, retIP, prevFP, narg, nret)
	if funcName != "" {
		header += " (" + funcName + ")"
	}
	// Annotate retIP with source position.
	if di != nil && retIP >= 0 && retIP < len(code) {
		if loc := di.PosToLine(code[retIP].Pos); loc != "" {
			header += " ret@" + loc
		}
	}
	_, _ = fmt.Fprintln(w, header+" ---")

	for i := funcAddr; i < sp; i++ {
		role := slotRole(i, fp, funcAddr, sp)
		marker := ""
		if i == fp {
			marker = " <- fp"
		}
		if i == sp-1 {
			marker += " <- sp"
		}

		// Annotate with symbol name.
		symName := ""
		if di != nil {
			switch {
			case i == funcAddr && funcName != "":
				symName = funcName
			case i > funcAddr && i < fp-frameOverhead:
				// Arg slot: look up in locals (args are the first locals).
				argIdx := i - funcAddr - 1
				symName = di.LocalName(funcName, argIdx+1)
			case i >= fp:
				localOff := i - fp + 1 + narg
				symName = di.LocalName(funcName, localOff)
			}
		}
		printSlot(w, i, role, mem[i], symName, marker)
	}
	_, _ = fmt.Fprintln(w)
}

// StackFrame is a single entry yielded by WalkCallStack.
type StackFrame struct {
	IP       int  // bytecode position within the frame's function
	Pos      Pos  // source position from m.code[IP], 0 if out of range
	TopLevel bool // synthetic frame for the top-level entry sequence (init / Eval driver)
}

// PanicError wraps a raw Go panic that escaped the VM (e.g. a reflect.Convert
// panic from inside the interpreter loop) with mvm-level diagnostic context.
// Frames is captured synchronously at the moment of the panic, before the
// Go stack unwinds m.fp.
type PanicError struct {
	Raw    any          // original panic value
	Pos    Pos          // source position of the panicking instruction
	IP     int          // bytecode IP at panic time
	Frames []StackFrame // captured before frame unwinding
	DI     *DebugInfo   // captured DebugInfo for formatting; may be nil
}

// Error renders the verbose layout (header + snippet + mvm stack) using the
// DebugInfo captured at panic time. Falls back to "panic: <raw>" if no
// DebugInfo was captured.
func (e *PanicError) Error() string {
	di := e.DI
	var b strings.Builder
	fmt.Fprintf(&b, "panic: %v\n", e.Raw)
	var funcName, loc string
	if di != nil {
		funcName = di.FuncAt(e.IP)
		loc = di.PosToLine(e.Pos)
	}
	switch {
	case funcName != "" && loc != "":
		fmt.Fprintf(&b, "  at %s (%s)\n", funcName, loc)
	case funcName != "":
		fmt.Fprintf(&b, "  at %s\n", funcName)
	case loc != "":
		fmt.Fprintf(&b, "  at %s\n", loc)
	}
	writeSourceSnippet(&b, di, e.Pos)
	if len(e.Frames) > 0 && di != nil {
		b.WriteString("\nmvm stack:\n")
		locW := 0
		type row struct{ loc, name string }
		rows := make([]row, 0, len(e.Frames))
		for _, f := range e.Frames {
			fLoc := di.PosToLine(f.Pos)
			fName := di.FuncAt(f.IP)
			if f.TopLevel && fName == "" {
				fName = "<init>"
			}
			rows = append(rows, row{fLoc, fName})
			if l := len(fLoc); l > locW {
				locW = l
			}
		}
		for _, r := range rows {
			fmt.Fprintf(&b, "  %-*s  %s\n", locW, r.loc, r.name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeSourceSnippet(b *strings.Builder, di *DebugInfo, pos Pos) {
	if di == nil || len(di.Sources) == 0 || pos == 0 {
		return
	}
	_, line, col := di.Sources.Resolve(int(pos))
	if line == 0 {
		return
	}
	text := di.Sources.LineText(int(pos))
	if text == "" && col == 0 {
		return
	}
	// Replace tabs with single spaces so the caret column aligns. col is
	// byte-based (matches LineText byte indexing).
	text = strings.ReplaceAll(text, "\t", " ")
	const maxWidth = 120
	caretCol := col
	if len(text) > maxWidth {
		// Window the line around caretCol if it's far to the right.
		start := 0
		if caretCol > maxWidth-20 {
			start = caretCol - (maxWidth - 20)
		}
		end := start + maxWidth
		if end > len(text) {
			end = len(text)
		}
		if start > 0 {
			text = "..." + text[start:end]
			caretCol = caretCol - start + 3
		} else {
			text = text[:end] + "..."
		}
	}
	prefix := fmt.Sprintf("  %d | ", line)
	fmt.Fprintf(b, "\n%s%s\n", prefix, text)
	if caretCol > 0 {
		b.WriteString(strings.Repeat(" ", len(prefix)+caretCol-1))
		b.WriteString("^\n")
	}
}

func (m *Machine) capturePanic(raw any) *PanicError {
	pe := &PanicError{Raw: raw, IP: m.ip}
	if m.ip >= 0 && m.ip < len(m.code) {
		pe.Pos = m.code[m.ip].Pos
	}
	if m.debugInfoFn != nil {
		pe.DI = m.debugInfoFn()
	}
	first := true
	m.WalkCallStack(func(f StackFrame) bool {
		if first {
			// WalkCallStack reports m.ip-1 for the innermost frame (the
			// "just-executed" instruction). At panic time m.ip is the
			// instruction that blew up mid-execution; report it directly.
			f.IP, f.Pos = pe.IP, pe.Pos
			first = false
		}
		pe.Frames = append(pe.Frames, f)
		return true
	})
	return pe
}

// WalkCallStack invokes yield for each call frame from innermost (the
// currently running function) to outermost. The first yielded frame's
// IP is m.ip-1 (the just-executed or about-to-execute instruction);
// each subsequent frame's IP is the call instruction in the caller
// (retIP-1 of the inner frame). yield returns false to stop early.
//
// After the topmost compiled-function frame, the top-level entry
// sequence (where Eval pushes Call/Exit instructions to drive init
// funcs and main) is also yielded as a synthetic frame so that
// runtime.Callers can observe init-time call sites.
func (m *Machine) WalkCallStack(yield func(StackFrame) bool) {
	fp := m.fp
	if fp == 0 {
		return
	}
	mem := m.mem
	pc := m.ip - 1
	for fp > 0 {
		var pos Pos
		if pc >= 0 && pc < len(m.code) {
			pos = m.code[pc].Pos
		}
		if !yield(StackFrame{IP: pc, Pos: pos}) {
			return
		}
		if fp-2 < 0 || fp-2 >= len(mem) {
			return
		}
		retIPInfo := mem[fp-2].num
		retIP := int(int32(retIPInfo)) //nolint:gosec
		pc = retIP - 1
		if fp-1 < 0 || fp-1 >= len(mem) {
			return
		}
		fp = int(mem[fp-1].num &^ (1 << 63)) //nolint:gosec
	}
	// Synthetic outermost frame for the top-level entry sequence (only
	// when it carries a real source position; CallFunc-synthesized Call
	// instructions have Pos==0 and represent the test harness driver, not
	// a user-visible frame).
	if pc >= 0 && pc < len(m.code) {
		if pos := m.code[pc].Pos; pos != 0 {
			yield(StackFrame{IP: pc, Pos: pos, TopLevel: true})
		}
	}
}

// DumpCallStack walks the frame pointer chain and prints every frame.
func (m *Machine) DumpCallStack(w io.Writer, di *DebugInfo) {
	mem := m.mem
	fp := m.fp
	sp := len(mem)

	if fp == 0 {
		_, _ = fmt.Fprintln(w, "--- no call frames (fp=0) ---")
		return
	}

	_, _ = fmt.Fprintln(w, "=== Call Stack ===")

	for fp > 0 {
		if fp-2 < 0 || fp-2 >= len(mem) {
			break
		}
		retIPInfo := mem[fp-2].num
		nret := int((retIPInfo >> 32) & 0xFFFF)
		narg := int(retIPInfo >> 48)

		DumpFrame(w, mem, m.code, fp, sp, narg, nret, di)

		// Walk to the previous frame.
		sp = fp - frameOverhead - narg - 1
		if fp-1 < 0 || fp-1 >= len(mem) {
			break
		}
		fpVal := mem[fp-1].num
		fp = int(fpVal &^ (1 << 63)) //nolint:gosec // mask off envSavedFlag
	}

	// Print globals with names if available.
	if di != nil && len(di.Globals) > 0 {
		_, _ = fmt.Fprintln(w, "--- Globals ---")
		indices := make([]int, 0, len(di.Globals))
		for idx := range di.Globals {
			if idx >= 0 && idx < len(mem) {
				indices = append(indices, idx)
			}
		}
		sort.Ints(indices)
		for _, idx := range indices {
			printSlot(w, idx, "global", mem[idx], di.Globals[idx], "")
		}
		_, _ = fmt.Fprintln(w)
	}
}

// DumpFrameStderr is a convenience wrapper that prints to stderr.
func DumpFrameStderr(mem []Value, code Code, fp, sp, narg, nret int, di *DebugInfo) {
	DumpFrame(os.Stderr, mem, code, fp, sp, narg, nret, di)
}

// DumpCallStackStderr is a convenience wrapper that prints to stderr.
func (m *Machine) DumpCallStackStderr(di *DebugInfo) {
	m.DumpCallStack(os.Stderr, di)
}

func slotRole(i, fp, funcAddr, sp int) string {
	switch {
	case i == funcAddr:
		return "func"
	case i > funcAddr && i < fp-frameOverhead:
		return fmt.Sprintf("arg %d", i-funcAddr-1)
	case i == fp-frameOverhead:
		return "deferHead"
	case i == fp-frameOverhead+1:
		return "retIP"
	case i == fp-frameOverhead+2:
		return "prevFP"
	case i >= fp && i < sp:
		return fmt.Sprintf("local %d", i-fp+1)
	default:
		return "?"
	}
}

func printSlot(w io.Writer, addr int, role string, v Value, symName, marker string) {
	var typStr, valStr string
	switch {
	case v.ref.IsValid():
		typStr = v.ref.Type().String()
		valStr = formatValue(v)
	case v.num != 0:
		typStr = "raw"
		valStr = strconv.FormatUint(v.num, 10)
	default:
		typStr = "zero"
		valStr = "0"
	}
	sym := ""
	if symName != "" {
		sym = " // " + symName
	}
	_, _ = fmt.Fprintf(w, "  mem[%-3d] %-12s %-10s %s%s%s\n", addr, role, typStr, valStr, sym, marker)
}

func formatValue(v Value) string {
	if !v.ref.IsValid() {
		return "<nil>"
	}
	if isNum(v.ref.Kind()) {
		return fmt.Sprintf("%v", v.Interface())
	}
	s := fmt.Sprintf("%v", v.Interface())
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

// enterDebug runs an interactive debug session. The Machine state (mem, ip, fp)
// must be synced before calling. On return, ip is set to resume execution.
func (m *Machine) enterDebug() {
	in := m.debugIn
	if in == nil {
		in = os.Stdin
	}
	out := m.debugOut
	if out == nil {
		out = os.Stderr
	}

	var di *DebugInfo
	if m.debugInfoFn != nil {
		di = m.debugInfoFn()
	}

	loc := ""
	if di != nil && m.ip > 0 && m.ip-1 < len(m.code) {
		loc = di.PosToLine(m.code[m.ip-1].Pos)
	}
	if loc != "" {
		_, _ = fmt.Fprintf(out, "trap at ip=%d (%s)\n", m.ip-1, loc)
	} else {
		_, _ = fmt.Fprintf(out, "trap at ip=%d\n", m.ip-1)
	}

	scanner := bufio.NewScanner(in)
	for {
		_, _ = fmt.Fprint(out, "debug> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "h", "help":
			_, _ = fmt.Fprintln(out, "  stack, bt  - dump call stack")
			_, _ = fmt.Fprintln(out, "  cont, c    - continue execution")
			_, _ = fmt.Fprintln(out, "  help, h    - show this help")
		case "bt", "stack":
			m.DumpCallStack(out, di)
		case "c", "cont":
			return
		case "":
			continue
		default:
			_, _ = fmt.Fprintf(out, "unknown command: %s (type 'help')\n", line)
		}
	}
}
