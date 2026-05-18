// The arm64 ABI keeps the current goroutine's *g pointer in R28, which
// the Go assembler exposes as the symbolic register `g`. We read it
// directly and return it as an unsafe.Pointer.

#include "textflag.h"

// func gptr() unsafe.Pointer
TEXT ·gptr(SB), NOSPLIT|NOFRAME, $0-8
	MOVD	g, ret+0(FP)
	RET
