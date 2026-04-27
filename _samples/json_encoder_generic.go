package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
)

func unmarshalJSON[T any](b []byte, x *[]T) error {
	if *x != nil {
		return errors.New("already initialized")
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, x)
}

type Slice[T any] struct{ x []T }

func (v Slice[T]) MarshalJSON() ([]byte, error)  { return json.Marshal(v.x) }
func (v *Slice[T]) UnmarshalJSON(b []byte) error { return unmarshalJSON(b, &v.x) }

type wrap struct {
	N  int
	S  Slice[string]
	SP *Slice[string] `json:",omitempty"`
}

func main() {
	ss := Slice[string]{x: []string{"bar"}}
	in := wrap{N: 1, S: ss, SP: &ss}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(&in)
	var got wrap
	_ = json.NewDecoder(&buf).Decode(&got)
	println(reflect.DeepEqual(got, in))
}

// Output:
// true
