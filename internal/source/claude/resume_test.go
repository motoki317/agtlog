package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

func TestPartCacheChunkedFixtureParity(t *testing.T) {
	err := filepath.WalkDir("testdata", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return err
		}
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertChunkedPartsParity(t, content)
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("generated/queued-commands", func(t *testing.T) {
		assertChunkedPartsParity(t, []byte(strings.Join(queuedCommandFixtureLines(), "\n")+"\n"))
	})
}

func assertChunkedPartsParity(t *testing.T, content []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session-chunked.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	parser, parts := testParser(), newMemoryParts()
	assertPartsParity(t, parser, path, parts)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ends := map[int]bool{len(content): true}
	start := 0
	for i, char := range content {
		if char == '\n' {
			ends[i] = true
			ends[i+1] = true
			ends[start+(i-start)/2] = true
			start = i + 1
		}
	}
	for end := 1; end < len(content); end += 137 {
		ends[end] = true
	}
	ordered := make([]int, 0, len(ends))
	for end := range ends {
		ordered = append(ordered, end)
	}
	sort.Ints(ordered)
	previous := 0
	for _, end := range ordered {
		if _, err := file.Write(content[previous:end]); err != nil {
			t.Fatal(err)
		}
		assertResumedPartsParity(t, parser, path, parts)
		assertResumedPartsParity(t, testParser(), path, parts)
		var part filePart
		if err := json.Unmarshal(parts.data[path], &part); err != nil {
			t.Fatal(err)
		}
		wantOffset := int64(bytes.LastIndexByte(content[:end], '\n') + 1)
		if part.Checkpoint.ResumeAt != wantOffset {
			t.Fatalf("chunk at %d: offset = %d, want %d", end, part.Checkpoint.ResumeAt, wantOffset)
		}
		previous = end
	}
}

func TestPartCacheRetainsCostErrorAndSpeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-usage.jsonl")
	speedLine := `{"type":"assistant","sessionId":"usage","requestId":"request-speed","message":{"id":"message-speed","model":"claude-opus-4-8","usage":{"input_tokens":2}}}`
	content := []byte(`{"type":"assistant","sessionId":"usage","costUSD":1.23,"message":{"id":"message-cost","model":"unknown-model","usage":{"input_tokens":10}}}` + "\n" +
		`{"type":"assistant","sessionId":"usage","isApiErrorMessage":true,"message":{"id":"message-error","model":"claude-opus-4-8","usage":{"input_tokens":1}}}` + "\n" + speedLine + "\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	assertUsage := func(session *model.Session, speed string) {
		t.Helper()
		if !session.HasError || len(session.Usage) != 3 || len(session.Requests) != 3 {
			t.Fatalf("persisted error or dedup differs: %#v", session)
		}
		if session.Usage[0].CostUSD == nil || *session.Usage[0].CostUSD != 1.23 || session.ModelCosts["unknown-model"] != 1.23 {
			t.Fatalf("persisted recorded cost differs: %#v / %#v", session.Usage[0], session.ModelCosts)
		}
		if session.Usage[2].Speed != speed || session.Requests[2].Usage.Speed != speed {
			t.Fatalf("persisted speed = %q / %q, want %q", session.Usage[2].Speed, session.Requests[2].Usage.Speed, speed)
		}
	}
	parser, parts := testParser(), newMemoryParts()
	assertUsage(assertPartsParity(t, parser, path, parts), "")
	assertUsage(assertResumedPartsParity(t, testParser(), path, parts), "")
	fastLine := strings.Replace(speedLine, `"input_tokens":2`, `"input_tokens":2,"speed":"fast"`, 1)
	if err := os.WriteFile(path, append(content, []byte(fastLine+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	assertUsage(assertResumedPartsParity(t, testParser(), path, parts), "fast")
	assertUsage(assertResumedPartsParity(t, testParser(), path, parts), "fast")
}

func TestPartCacheUnterminatedTail(t *testing.T) {
	for _, suffix := range []string{"", "\r"} {
		t.Run("suffix-"+suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session-tail.jsonl")
			content := []byte("{\"type\":\"user\",\"sessionId\":\"tail\",\"message\":{\"content\":\"Survey 🌙\"}}\r\n" +
				"{\"type\":\"assistant\",\"message\":{\"id\":\"tail-message\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":7}}}" + suffix)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			parser, parts := testParser(), newMemoryParts()
			assertPartsParity(t, parser, path, parts)
			assertPartsParity(t, testParser(), path, parts)
			var part filePart
			if err := json.Unmarshal(parts.data[path], &part); err != nil {
				t.Fatal(err)
			}
			if len(part.Accumulator.UsageRecords) != 0 {
				t.Fatal("stored usage from unterminated line")
			}
			if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			assertPartsParity(t, parser, path, parts)
		})
	}
}

func TestPartCacheCheckpointFallback(t *testing.T) {
	head := "{\"type\":\"user\",\"sessionId\":\"original\",\"message\":{\"content\":\"Survey river\"}}\n"
	filler := strings.Repeat(" ", jsonl.CheckpointHeadBytes) + "\n"
	last := "{\"type\":\"assistant\",\"message\":{\"id\":\"last\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":7}}}\n"
	base := []byte(head + filler + last)
	tests := []struct {
		name   string
		change func([]byte) []byte
	}{
		{"truncation", func(b []byte) []byte { return b[:len(head)] }},
		{"head changed", func(b []byte) []byte { return bytes.Replace(b, []byte("original"), []byte("replaced"), 1) }},
		{"boundary changed", func(b []byte) []byte { return bytes.Replace(b, []byte("tokens\":7"), []byte("tokens\":8"), 1) }},
		{"lost newline", func(b []byte) []byte { b[len(b)-1] = ' '; return b }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session-fallback.jsonl")
			if err := os.WriteFile(path, base, 0o600); err != nil {
				t.Fatal(err)
			}
			parser, parts := testParser(), newMemoryParts()
			assertPartsParity(t, parser, path, parts)
			var old filePart
			if err := json.Unmarshal(parts.data[path], &old); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.change(bytes.Clone(base)), 0o600); err != nil {
				t.Fatal(err)
			}
			resumed := false
			got, _, err := parseFilePart(context.Background(), path, &old, func() { resumed = true })
			if err != nil {
				t.Fatal(err)
			}
			if resumed {
				t.Fatal("invalid checkpoint entered resume scan")
			}
			session, names, prompt, err := got.finish(context.Background())
			want, wantNames, wantPrompt, wantErr := parser.parseFile(context.Background(), path)
			if err != nil || wantErr != nil || !reflect.DeepEqual(session, want) || !reflect.DeepEqual(names, wantNames) || prompt != wantPrompt {
				t.Fatalf("fallback differs: %v, %v", err, wantErr)
			}
			assertPartsParity(t, parser, path, parts)
		})
	}
}

func TestPartCacheEveryByteParity(t *testing.T) {
	content := []byte("\nmalformed\n" +
		"{\"type\":\"user\",\"sessionId\":\"survey\",\"message\":{\"content\":\"Survey 🌙\"}}\r\n" +
		"{\"type\":\"assistant\",\"isSidechain\":true,\"message\":{\"id\":\"survey-message\",\"model\":\"claude-fable-5\",\"usage\":{\"input_tokens\":100}}}\n" +
		"{\"type\":\"assistant\",\"requestId\":\"survey-request\",\"message\":{\"id\":\"survey-message\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":7,\"iterations\":[{\"type\":\"advisor_message\",\"model\":\"claude-fable-5\",\"input_tokens\":3}]}}}\r\n" +
		"{\"type\":\"ai-title\",\"aiTitle\":\"River survey\"}")
	path := filepath.Join(t.TempDir(), "session-bytes.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	parts := newMemoryParts()
	assertPartsParity(t, testParser(), path, parts)
	for i, char := range content {
		if _, err := file.Write([]byte{char}); err != nil {
			t.Fatal(err)
		}
		t.Run(strconv.Itoa(i+1), func(t *testing.T) {
			assertResumedPartsParity(t, testParser(), path, parts)
			assertResumedPartsParity(t, testParser(), path, parts)
		})
	}
}

type scanMutationContext struct {
	context.Context
	mutate func()
}

func (c scanMutationContext) Err() error {
	c.mutate()
	return c.Context.Err()
}

func TestPartCacheRangeRewriteDuringScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-range.jsonl")
	head := []byte("{\"type\":\"user\",\"sessionId\":\"range\",\"message\":{\"content\":\"Survey river\"}}\n")
	if err := os.WriteFile(path, head, 0o600); err != nil {
		t.Fatal(err)
	}
	_, previous, err := parseFilePart(context.Background(), path, nil, nil)
	if err != nil || previous == nil {
		t.Fatalf("initial parse: %v, %v", previous, err)
	}
	tail := []byte("{\"type\":\"assistant\",\"message\":{\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":7}}}\n")
	if err := os.WriteFile(path, append(bytes.Clone(head), tail...), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated := false
	ctx := scanMutationContext{Context: context.Background(), mutate: func() {
		if mutated || len(previous.Accumulator.UsageRecords) == 0 {
			return
		}
		mutated = true
		replacement := bytes.Replace(tail, []byte("tokens\":7"), []byte("tokens\":9"), 1)
		if err := os.WriteFile(path, append(bytes.Clone(head), replacement...), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	result, next, err := parseFilePart(ctx, path, previous, nil)
	if err != nil || next == nil || !mutated || next.Accumulator == previous.Accumulator {
		t.Fatalf("range rewrite fallback: part = %v, mutation = %v, error = %v", next, mutated, err)
	}
	got, _, _, err := result.finish(context.Background())
	want, _, _, wantErr := testParser().parseFile(context.Background(), path)
	if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("range rewrite parse differs: %v, %v", err, wantErr)
	}
}

func TestPartCacheOversizedBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-large-line.jsonl")
	head := []byte("{\"type\":\"user\",\"sessionId\":\"large\",\"message\":{\"content\":\"Survey river\"}}\n")
	boundary := []byte(strings.Repeat(" ", jsonl.MaxLineBytes) + "\n")
	tail := []byte("{\"type\":\"assistant\",\"message\":{\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":7}}}")
	content := append(append(bytes.Clone(head), boundary...), tail...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	parser, parts := testParser(), newMemoryParts()
	assertPartsParity(t, parser, path, parts)
	assertPartsParity(t, parser, path, parts)
	var previous filePart
	if err := json.Unmarshal(parts.data[path], &previous); err != nil {
		t.Fatal(err)
	}
	if previous.Checkpoint.LastLineBytes != int64(len(boundary)) || previous.Checkpoint.LastLineHash != sha256.Sum256(boundary) {
		t.Fatalf("oversized boundary checkpoint = %#v", previous.Checkpoint)
	}
	content[len(head)+jsonl.CheckpointHeadBytes] = 'x'
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	resumed := false
	if _, _, err := parseFilePart(context.Background(), path, &previous, func() { resumed = true }); err != nil || resumed {
		t.Fatalf("changed oversized boundary: resumed = %v, error = %v", resumed, err)
	}
	assertPartsParity(t, parser, path, parts)
	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPartsParity(t, parser, path, parts)
}

func TestPartCacheRewriteAfterValidation(t *testing.T) {
	original := []byte("{\"type\":\"user\",\"sessionId\":\"original\",\"message\":{\"content\":\"Survey river\"}}\n")
	replacement := bytes.Replace(original, []byte("original"), []byte("replaced"), 1)
	for _, atomic := range []bool{false, true} {
		t.Run(map[bool]string{false: "in place", true: "atomic"}[atomic], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session-rewrite.jsonl")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			_, previous, err := parseFilePart(context.Background(), path, nil, nil)
			if err != nil || previous == nil {
				t.Fatalf("initial parse: %v, %v", previous, err)
			}
			called := false
			result, next, err := parseFilePart(context.Background(), path, previous, func() {
				called = true
				target := path
				if atomic {
					target += ".replacement"
				}
				if err := os.WriteFile(target, replacement, 0o600); err != nil {
					t.Fatal(err)
				}
				if atomic {
					if err := os.Rename(target, path); err != nil {
						t.Fatal(err)
					}
				}
			})
			if err != nil || next == nil || !called || next.Accumulator == previous.Accumulator {
				t.Fatalf("rewrite fallback: part = %v, hook = %v, error = %v", next, called, err)
			}
			got, _, _, err := result.finish(context.Background())
			want, _, _, wantErr := testParser().parseFile(context.Background(), path)
			if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("rewrite parse differs: %v, %v", err, wantErr)
			}
		})
	}
}
