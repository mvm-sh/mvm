// Go 1.17+ keeps the current goroutine's *g pointer in R14 on amd64
// (Go internal ABI register convention). We read it directly and return
// it as an unsafe.Pointer.

#include "textflag.h"

// func gptr() unsafe.Pointer
TEXT ·gptr(SB), NOSPLIT|NOFRAME, $0-8
	MOVQ	R14, ret+0(FP)
	RET
