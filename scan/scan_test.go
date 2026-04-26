package scan_test

import (
	"log"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/scan"
)

var GoScanner *scan.Scanner

func init() {
	log.SetFlags(log.Lshortfile)
	GoScanner = scan.NewScanner(golang.GoSpec)
}

func TestScan(t *testing.T) {
	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			errStr := ""
			tokens, err := GoScanner.Scan(test.src, true)
			if err != nil {
				errStr = err.Error()
			}
			if errStr != test.err {
				t.Errorf("got error %v, want error %#v", errStr, test.err)
			}
			if result := tokStr(tokens); result != test.tok {
				t.Errorf("got %v, want %v", result, test.tok)
			}
		})
	}
}

func tokStr(tokens []scan.Token) (s string) {
	var sb strings.Builder
	for _, t := range tokens {
		sb.WriteString(t.String() + " ")
	}
	s += sb.String()
	return s
}

var tests = []struct{ n, src, tok, err string }{
	{n: "#00", src: ""},
	{n: "#01", src: "   abc + 5", tok: `Ident"abc" Add Int"5" Semicolon `},
	{n: "#02", src: "abc0+5 ", tok: `Ident"abc0" Add Int"5" Semicolon `},
	{n: "#03", src: "a+5\na=x-4", tok: `Ident"a" Add Int"5" Semicolon Ident"a" Assign Ident"x" Sub Int"4" Semicolon `},
	{n: "#04", src: `return "hello world" + 4`, tok: `Return String"\"hello world\"" Add Int"4" Semicolon `},
	{n: "#05", src: `print(4 * (3+7))`, tok: `Ident"print" ParenBlock"(4 * (3+7))" Semicolon `},
	{n: "#06", src: `"foo`, err: "1:1: block not terminated"},
	{n: "#07", src: `abc
def "foo truc`, err: "2:5: block not terminated"},
	{n: "#08", src: `"ab\"`, err: "1:1: block not terminated"},
	{n: "#09", src: `"ab\\"`, tok: `String"\"ab\\\\\"" Semicolon `},
	{n: "#10", src: `"ab\\\"`, err: "1:1: block not terminated"},
	{n: "#11", src: `"ab\\\\"`, tok: `String"\"ab\\\\\\\\\"" Semicolon `},
	{n: "#12", src: `"abc
def"`, err: "1:1: block not terminated"},
	{n: "#13", src: "`hello\nworld`", tok: "String\"`hello\\nworld`\" Semicolon "},
	{n: "#14", src: "2* (3+4", err: "1:4: block not terminated"},
	{n: "#15", src: `("fo)o")+1`, tok: `ParenBlock"(\"fo)o\")" Add Int"1" Semicolon `},
	{n: "#16", src: `"foo""bar"`, tok: `String"\"foo\"" String"\"bar\"" Semicolon `},
	{n: "#17", src: "/* a comment */ a = 2", tok: `Comment"/* a comment */" Ident"a" Assign Int"2" Semicolon `},
	{n: "#18", src: "return // quit\nbegin", tok: `Return Comment"// quit" Semicolon Ident"begin" Semicolon `},
	{n: "#19", src: "return // quit", tok: `Return Comment"// quit" Semicolon `},
	{n: "#20", src: "println(3 /* argh ) */)", tok: `Ident"println" ParenBlock"(3 /* argh ) */)" Semicolon `},
	{n: "#21", src: `println("in f")`, tok: `Ident"println" ParenBlock"(\"in f\")" Semicolon `},
	{n: "#22", src: "a, b = 1, 2", tok: `Ident"a" Comma Ident"b" Assign Int"1" Comma Int"2" Semicolon `},
	{n: "#23", src: "1 + \n2 + 3", tok: `Int"1" Add Int"2" Add Int"3" Semicolon `},
	{n: "#24", src: "i++\n2 + 3", tok: `Ident"i" Inc Semicolon Int"2" Add Int"3" Semicolon `},
	{n: "#25", src: "return\na = 1", tok: `Return Semicolon Ident"a" Assign Int"1" Semicolon `},
	{n: "#26", src: "if\na == 2 { return }", tok: `If Ident"a" Equal Int"2" BraceBlock"{ return }" Semicolon `},
	{n: "#27", src: "f(4)\nreturn", tok: `Ident"f" ParenBlock"(4)" Semicolon Return Semicolon `},
	{n: "#28", src: "f(3).\nfield", tok: `Ident"f" ParenBlock"(3)" Period"." Ident"field" Semicolon `},
	{n: "#29", src: "\n\n\tif i < 1 {return 0}", tok: `If Ident"i" Less Int"1" BraceBlock"{return 0}" Semicolon `},

	// Numbers: integers.
	{n: "#30", src: "0", tok: `Int"0" Semicolon `},
	{n: "#31", src: "42", tok: `Int"42" Semicolon `},
	{n: "#32", src: "1_000_000", tok: `Int"1_000_000" Semicolon `},

	// Numbers: floats.
	{n: "#33", src: "3.14", tok: `Float"3.14" Semicolon `},
	{n: "#34", src: "0.5", tok: `Float"0.5" Semicolon `},
	{n: "#35", src: "1e10", tok: `Float"1e10" Semicolon `},
	{n: "#36", src: "2.5E-3", tok: `Float"2.5E-3" Semicolon `},
	{n: "#37", src: "1.0e+7", tok: `Float"1.0e+7" Semicolon `},
	{n: "#37a", src: ".5", tok: `Float".5" Semicolon `},
	{n: "#37b", src: ".0312", tok: `Float".0312" Semicolon `},
	{n: "#37c", src: ".5e2", tok: `Float".5e2" Semicolon `},
	{n: "#37d", src: ".5 + 1", tok: `Float".5" Add Int"1" Semicolon `},

	// Numbers: hexadecimal.
	{n: "#38", src: "0xff", tok: `Int"0xff" Semicolon `},
	{n: "#39", src: "0XAB12", tok: `Int"0XAB12" Semicolon `},
	{n: "#40", src: "0x1_2_3", tok: `Int"0x1_2_3" Semicolon `},

	// Numbers: octal.
	{n: "#41", src: "0o77", tok: `Int"0o77" Semicolon `},
	{n: "#42", src: "0O17", tok: `Int"0O17" Semicolon `},

	// Numbers: binary.
	{n: "#43", src: "0b1010", tok: `Int"0b1010" Semicolon `},
	{n: "#44", src: "0B110_001", tok: `Int"0B110_001" Semicolon `},

	// Numbers: in expressions.
	{n: "#45", src: "0xff + 3.14", tok: `Int"0xff" Add Float"3.14" Semicolon `},
	{n: "#46", src: "123.String()", tok: `Int"123" Period"." Ident"String" ParenBlock"()" Semicolon `},

	// Non-ASCII identifiers (Go spec allows any Unicode letter).
	{n: "#47", src: "ж := 42", tok: `Ident"ж" Define Int"42" Semicolon `},
	{n: "#48", src: "café + 1", tok: `Ident"café" Add Int"1" Semicolon `},
	{n: "#49", src: "日本語", tok: `Ident"日本語" Semicolon `},
}
