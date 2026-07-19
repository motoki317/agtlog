package jsonl

import (
	"strings"
	"testing"
)

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
