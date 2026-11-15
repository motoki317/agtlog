package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

func TestFileAccumulatorFixtureParity(t *testing.T) {
	err := filepath.WalkDir("testdata", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return err
		}
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			parser := testParser()
			want, wantNames, wantPrompt, err := parser.parseFile(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			accumulator := newFileAccumulator(path)
			if err := jsonl.ForEachContext(ctx, bytes.NewReader(data), accumulator.ingest); err != nil {
				t.Fatal(err)
			}
			state, err := json.Marshal(accumulator)
			if err != nil {
				t.Fatal(err)
			}
			var restored fileAccumulator
			if err := jsonl.Unmarshal(state, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(accumulator, &restored) {
				t.Fatal("JSON round trip changed accumulator state")
			}
			for _, candidate := range []*fileAccumulator{accumulator, accumulator, &restored} {
				got, names, prompt, err := candidate.finish(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(names, wantNames) || prompt != wantPrompt {
					t.Fatalf("finish() = %#v, %v, %q, want %#v, %v, %q", got, names, prompt, want, wantNames, wantPrompt)
				}
				parser.calculator.ApplySession(got)
				got.Title = "Changed result"
				attachSubagent(got, &model.Session{UpdatedAt: got.UpdatedAt.Add(time.Hour)})
				if len(got.Usage) > 0 {
					got.Usage[0].InputTokens++
					got.Requests[0].MessageID = "changed-result"
					got.Models[0] = "changed-model"
				}
				names["changed-run"] = "Changed workflow"
			}
			if after, err := json.Marshal(accumulator); err != nil || !bytes.Equal(after, state) {
				t.Fatalf("finish or result mutation changed accumulator state: %v", err)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFileAccumulatorFinishRebuildsAfterAppend(t *testing.T) {
	accumulator := newFileAccumulator("session-survey.jsonl")
	steps := []struct {
		line string
		want model.RequestUsage
	}{
		{
			line: `{"type":"assistant","isSidechain":true,"message":{"id":"message-survey","model":"claude-fable-5","usage":{"input_tokens":100}}}`,
			want: model.RequestUsage{MessageID: "message-survey", Usage: model.Usage{Model: "claude-fable-5", InputTokens: 100}},
		},
		{
			line: `{"type":"assistant","requestId":"request-survey","message":{"id":"message-survey","model":"claude-opus-4-8","usage":{"input_tokens":2}}}`,
			want: model.RequestUsage{MessageID: "message-survey", RequestID: "request-survey", Usage: model.Usage{Model: "claude-opus-4-8", InputTokens: 2}},
		},
		{
			line: `{"type":"assistant","requestId":"request-survey","message":{"id":"message-survey","model":"claude-opus-4-8","usage":{"input_tokens":2,"output_tokens":3,"speed":"fast"}}}`,
			want: model.RequestUsage{MessageID: "message-survey", RequestID: "request-survey", Usage: model.Usage{Model: "claude-opus-4-8", InputTokens: 2, OutputTokens: 3, Speed: "fast"}},
		},
	}
	var results []*model.Session
	for _, step := range steps {
		accumulator.ingest([]byte(step.line))
		session, _, _, err := accumulator.finish(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, session)
	}
	for i, session := range results {
		want := steps[i].want
		if !reflect.DeepEqual(session.Usage, []model.Usage{want.Usage}) ||
			!reflect.DeepEqual(session.Requests, []model.RequestUsage{want}) ||
			!reflect.DeepEqual(session.Models, []string{want.Usage.Model}) {
			t.Errorf("step %d: usage = %#v, requests = %#v, models = %v, want only %#v", i, session.Usage, session.Requests, session.Models, want)
		}
	}
}

func TestFileAccumulatorFinishEmptyAndCanceled(t *testing.T) {
	accumulator := newFileAccumulator("session-empty.jsonl")
	want := &model.Session{Agent: model.AgentClaude, Path: "session-empty.jsonl"}
	session, names, prompt, err := accumulator.finish(context.Background())
	if err != nil || !reflect.DeepEqual(session, want) || len(names) != 0 || prompt != "" {
		t.Fatalf("empty finish() = %#v, %v, %q, %v", session, names, prompt, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := accumulator.finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled finish() error = %v, want context canceled", err)
	}
}
