package jsonl

import (
	jsoniter "github.com/json-iterator/go"
)

// decoder avoids encoding/json, which validates the whole payload again on every
// call. The parsers decode one record several times, and records carry
// multi-megabyte tool output, so that validation dominated the time to open a
// session detail.
var decoder = jsoniter.ConfigCompatibleWithStandardLibrary

// Unmarshal stops at the first field whose JSON shape does not match its Go
// type, and the fields after it stay unset. encoding/json fills them before it
// returns the error. Declare a field whose shape varies as json.RawMessage, and
// decode it where the shape is known.
func Unmarshal(data []byte, value any) error {
	return decoder.Unmarshal(data, value)
}
