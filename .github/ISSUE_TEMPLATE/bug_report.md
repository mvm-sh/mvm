---
name: Bug report
about: A program that runs differently under mvm than under `go run`
title: ''
labels: bug
---

### What happened

<!-- One or two sentences. Include the mvm error message if any. -->

### Minimal reproduction

<!--
A small Go program that exhibits the bug. Prefer something that fits
in 20 lines and uses no third-party imports. If you can save it as a
file, paste the content; if it only reproduces in the REPL, paste the
session.
-->

```go
package main

func main() {
    // ...
}
```

### What you expected

<!-- What `go run` (or `go test`) prints for the same program. -->

### Environment

- mvm version / commit:
- Go version (`go version`):
- OS / arch:
