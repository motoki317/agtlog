package jsonl

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestForEachContextWithOffsetRoundTripsAcrossOversizedLine(t *testing.T) {
	oversized := strings.Repeat("x", maxLineBytes+1)
	input := []byte("first\r\n" + oversized + "\nlast")
	type record struct {
		line           string
		offset, length int64
	}
	var got []record

	err := ForEachContextWithOffset(context.Background(), bytes.NewReader(input), func(line []byte, offset, length int64) {
		got = append(got, record{line: string(line), offset: offset, length: length})
	})
	if err != nil {
		t.Fatalf("ForEachContextWithOffset() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ForEachContextWithOffset() records = %#v, want first and last", got)
	}
	for _, record := range got {
		roundTrip := input[record.offset : record.offset+record.length]
		if !bytes.Equal(roundTrip, []byte(record.line)) {
			t.Errorf("input[%d:%d] = %q, want %q", record.offset, record.offset+record.length, roundTrip, record.line)
		}
	}
	wantLastOffset := int64(len("first\r\n") + len(oversized) + len("\n"))
	if got[1].offset != wantLastOffset || got[1].length != int64(len("last")) {
		t.Fatalf("last coordinate = (%d, %d), want (%d, %d)", got[1].offset, got[1].length, wantLastOffset, len("last"))
	}
}

func TestForEachSkipsOversizedLineAndContinues(t *testing.T) {
	input := "first\n" + strings.Repeat("x", maxLineBytes+1) + "\nlast\n"
	var got []string

	if err := ForEach(strings.NewReader(input), func(line []byte) {
		got = append(got, string(line))
	}); err != nil {
		t.Fatalf("ForEach() error = %v", err)
	}
	if strings.Join(got, ",") != "first,last" {
		t.Fatalf("ForEach() lines = %v, want first,last", got)
	}
}

func TestForEachVisitsFinalLineWithoutNewline(t *testing.T) {
	var got string
	if err := ForEach(strings.NewReader("final"), func(line []byte) { got = string(line) }); err != nil {
		t.Fatalf("ForEach() error = %v", err)
	}
	if got != "final" {
		t.Fatalf("ForEach() line = %q, want final", got)
	}
}
