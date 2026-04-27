package main

import (
	"encoding/json"
	"fmt"
)

type MyStr struct{ V string }

func (s MyStr) MarshalJSON() ([]byte, error) { return json.Marshal("!" + s.V + "!") }

func (s *MyStr) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if len(v) >= 2 && v[0] == '!' && v[len(v)-1] == '!' {
		v = v[1 : len(v)-1]
	}
	s.V = v
	return nil
}

type Wrap struct {
	Inner MyStr
}

func main() {
	orig := Wrap{Inner: MyStr{V: "x"}}
	b, _ := json.Marshal(orig)
	fmt.Println("encoded:", string(b))
	var got Wrap
	if err := json.Unmarshal(b, &got); err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Println("decoded:", got.Inner.V)
}

// Output:
// encoded: {"Inner":"!x!"}
// decoded: x
