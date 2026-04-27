package main

import "context"

func get(ctx context.Context, k string) string {
	return ctx.Value(k).(string)
}

func main() {
	ctx := context.WithValue(context.Background(), "hello", "world")
	println(get(ctx, "hello"))
}

// Output:
// world
