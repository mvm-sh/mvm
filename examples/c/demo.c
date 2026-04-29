// Small C program that embeds the mvm interpreter.
//
// Build with the accompanying Makefile, then run ./demo.

#include <stdio.h>
#include <stdlib.h>

#include "libmvm.h"

static int run(uintptr_t i, const char *src) {
    char *result = NULL;
    char *error  = NULL;
    if (MvmEval(i, (char *)"m:expr", (char *)src, &result, &error) == 0) {
        printf("> %s\n  = %s\n", src, result);
        MvmFreeString(result);
        return 0;
    }
    fprintf(stderr, "error evaluating %s: %s\n", src, error);
    MvmFreeString(error);
    return 1;
}

int main(void) {
    uintptr_t i = MvmNew();

    run(i, "1 + 2 + 3");
    run(i, "host.Greet(\"world\")");
    run(i, "strings.ToUpper(host.Repeat(\"ab\", 3))");
    run(i, "fmt.Sprintf(\"answer = %d\", host.Answer)");

    MvmFree(i);
    return 0;
}
