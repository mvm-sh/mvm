# vm

`vm` is a bytecode based stack machine.

The purpose of `vm` is to provide a simple, fast, embeddable and
portable Go execution environment.

```mermaid
graph LR
s[ ] --> |source| a(scanner)
--> |tokens| b(parser)
--> |AST| c(codegen)
--> |bytecode| d[vm]
subgraph vm
    d
end
style s height:0px;
```

The bytecode consists of a dozen of instructions, each taking 0 or 1
immediate argument (non-immediate arguments are only passed through the
stack). Only a few operators for a few types are implemented. I expect
to have 1 instruction per operator per numerical type, all with the same
pattern, which would be generated from a template. Estimate is around 20
operators and 10 numerical types, so around 200 instructions in final.

Structurally, the vm implements logical and arithmetic operators,
condional jumps for `if`, `for` and `switch` control flow, and function
call, return and frame management.

the memory state of the vm is a slice of (`[]Value`), where `Value` is similar
to a `reflect.Value`, containing pointers to value data and type.

The whole vm is coded in a single function of 80 lines with no
dependencies. The size will grow as we add missing instructions, but the
code complexity will remain the same.

the vm1 package is totally standalone and could be used for any purpose
outside of mvm and/or gno.

## Instructions

| Opcode | Parameters   | Stack (before : after) | Description                                   |
|--------|--------------|------------------------|-----------------------------------------------|
| `Get`  | local, index | ` : v`                 | Get value at address and push it on the stack |
| `Set`  | local, index | `v : `                 | Pop stack and set value at address            |
| `Add`  |              | `a b : c`              | Pop `a`, `b` and push (a+b)                   |

Memory addresses passed to instructions are computed from a tuple
of integers: `local` and `index`. `local` can be 0 or 1. if 0,
`index` is the offset from the start of memory, else `index` is
the offset from the frame pointer (`fp` register).
