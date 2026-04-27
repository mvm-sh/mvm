package main

import (
	"encoding/json"
	"fmt"
)

type MyStr struct{ V string }

func (s MyStr) MarshalJSON() ([]byte, error) { return json.Marshal("!" + s.V + "!") }

type Wrap struct {
	Inner MyStr
}

func main() {
	top, _ := json.Marshal(MyStr{V: "x"})
	fmt.Println("top-level:", string(top))
	nested, _ := json.Marshal(Wrap{Inner: MyStr{V: "x"}})
	fmt.Println("nested:   ", string(nested))
}

// Output:
// top-level: "!x!"
// nested:    {"Inner":"!x!"}
