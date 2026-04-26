# Scanner

A scanner takes a string in input and returns an array of tokens.

```mermaid
graph LR
s[ ] --> |source| a(scanner)
--> |tokens| b(parser)
--> |AST| c[ ]
subgraph scanner
    a
end
style s height:0px;
style c height:0px;
```

Tokens can be of the following kinds:
- identifier
- number
- operator
- separator
- string
- block

Resolving nested blocks in the scanner is making the parser simple
and generic, without having to resort to parse tables.

The lexical rules are provided by a language specification at language
level which includes the following:

- a set of composable properties (1 per bit, on an integer) for each
  character in the ASCII range (where all separator, operators and
  reserved keywords must be defined).
- for each block or string, the specification of starting and ending
  delimiter.

## Development status

A successful test must be provided to check the status.

- [x] numbers starting with a digit
- [ ] numbers starting otherwise
- [x] unescaped strings (including multiline)
- [x] escaped string (including multiline)
- [x] separators (in UTF-8 range)
- [x] single line string (\n not allowed)
- [x] identifiers (in UTF-8 range)
- [x] operators, concatenated or not
- [x] single character block/string delimiters
- [x] arbitrarly nested blocks and strings
- [x] multiple characters block/string delimiters
- [x] semi-colon automatic insertion after newline
- [x] blocks delimited by operator characters
- [ ] blocks delimited by identifiers
- [x] blocks with delimiter inclusion/exclusion rules
- [ ] blocks delimited by indentation level (python, yaml, ...)
