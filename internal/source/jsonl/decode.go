package jsonl

import (
	jsoniter "github.com/json-iterator/go"
)

// decoder is configured for encoding/json semantics; the parsers decode the same
// record several times (the envelope, then nested content, tool input, tool
// output), and encoding/json re-validates the whole payload on every call.
// Records carry multi-megabyte tool output, so that scanning dominated the time
// to open a session detail.
var decoder = jsoniter.ConfigCompatibleWithStandardLibrary

// Unmarshal decodes a single JSONL record or one of its nested values.
//
// Unlike encoding/json it stops at the first field whose shape does not match,
// so a caller must not depend on the remaining fields still being filled after
// an error: give a field that varies in shape a json.RawMessage and decode it
// where the shape is known.
func Unmarshal(data []byte, value any) error {
	return decoder.Unmarshal(data, value)
}
