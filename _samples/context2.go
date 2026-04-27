package main

import (
	"context"
	"fmt"
)

func main() {
	ctx := context.WithValue(context.Background(), "a", "b")
	ctx = context.WithValue(ctx, "c", "d")
	fmt.Println(ctx)
}

// Output:
// context.Background.WithValue(a, b).WithValue(c, d)
