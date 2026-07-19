package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

func TestFormatSessionMarksEstimatedCost(t *testing.T) {
	session := &model.Session{
		Agent:    model.AgentCodex,
		Project:  "starship",
		Title:    "Design a rover",
		Models:   []string{"gpt-5.6-sol"},
		Messages: 1,
		Usage:    []model.Usage{{InputTokens: 250, OutputTokens: 40, CacheReadTokens: 50, InputIncludesCacheRead: true}},
		Cost:     model.Cost{USD: 1.25, Estimated: true},
	}

	want := "codex\tstarship\tDesign a rover\tgpt-5.6-sol\t1 msgs\t290 tokens\t~$1.2500"
	if got := formatSession(session); got != want {
		t.Fatalf("formatSession() = %q, want %q", got, want)
	}
}

type staticSource struct {
	session *model.Session
}

func (s staticSource) Agent() model.AgentKind { return s.session.Agent }
func (s staticSource) Roots() []string        { return nil }
func (s staticSource) Discover(context.Context) ([]string, error) {
	return []string{"session.jsonl"}, nil
}
func (s staticSource) Parse(string) (*model.Session, error) { return s.session, nil }

func TestRunPrintsDiscoveredSessions(t *testing.T) {
	session := &model.Session{
		ID:       "session-a",
		Agent:    model.AgentClaude,
		Project:  "starship",
		Title:    "Plan the launch",
		Models:   []string{"claude-opus-4-8"},
		Messages: 2,
		Usage:    []model.Usage{{InputTokens: 10, OutputTokens: 2}},
		Cost:     model.Cost{USD: 0.5},
	}
	registry := source.NewRegistry([]source.Source{staticSource{session: session}}, source.Options{Workers: 1})
	var output bytes.Buffer

	if err := run(context.Background(), nil, &output, registry); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	want := "claude\tstarship\tPlan the launch\tclaude-opus-4-8\t2 msgs\t12 tokens\t$0.5000\n"
	if output.String() != want {
		t.Fatalf("run() output = %q, want %q", output.String(), want)
	}
}

func TestRunPrintsVersionWithoutDiscovery(t *testing.T) {
	registry := source.NewRegistry(nil, source.Options{})
	var output bytes.Buffer

	if err := run(context.Background(), []string{"--version"}, &output, registry); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if want := version + "\n"; output.String() != want {
		t.Fatalf("run() output = %q, want %q", output.String(), want)
	}
}
