---
name: One-liner bug report
about: A bug reproducible by a single mvm command (e.g. `mvm test <import-path>`, `mvm -e ...`)
title: 'failure of: '
labels: bug
---

<!--
Use this template when the bug reproduces from a single mvm command,
with no extra Go source needed. Examples:

  mvm test github.com/google/uuid
  mvm -e 'fmt.Println(1<<63)'
  mvm run _samples/foo.go

Paste the full command and its output below.
-->

```console
$ mvm ...
```

mvm version:
