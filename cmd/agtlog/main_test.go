package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
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

func TestFormatSessionKeepsTitleOnOneLine(t *testing.T) {
	session := &model.Session{
		Agent:   model.AgentClaude,
		Project: "starship",
		Title:   "Plan the launch\nwithout delay",
		Models:  []string{"claude-opus-4-8"},
	}

	want := "claude\tstarship\tPlan the launch without delay\tclaude-opus-4-8\t0 msgs\t0 tokens\t$0.0000"
	if got := formatSession(session); got != want {
		t.Fatalf("formatSession() = %q, want %q", got, want)
	}
}

func TestFormatSessionSanitizesEveryExternalField(t *testing.T) {
	session := &model.Session{
		Agent:   model.AgentKind("claude\nforged"),
		Project: "starship\x1b]8;;https://invalid.example\a\nforged",
		Title:   "safe\u202ereversed\rtitle",
		Models:  []string{"claude\tforged", "model\x1b[31mred"},
	}

	got := formatSession(session)
	if strings.Count(got, "\t") != 6 || strings.ContainsAny(got, "\r\n\x1b\a") || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("formatSession() emitted unsafe row %q", got)
	}
}

func TestFormatSessionMarksMissingPricing(t *testing.T) {
	session := &model.Session{
		Agent:  model.AgentClaude,
		Models: []string{"unknown-model"},
		Cost:   model.Cost{MissingPricingModels: []string{"unknown-model"}},
	}

	if got := formatSession(session); !strings.HasSuffix(got, "$unpriced(unknown-model)") {
		t.Fatalf("formatSession() = %q, want explicit unpriced marker", got)
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

func TestExecutePrintsVersionBeforeBuildingRegistry(t *testing.T) {
	var output bytes.Buffer
	called := false
	err := execute(context.Background(), []string{"--version"}, &output, func() (*source.Registry, error) {
		called = true
		return nil, errors.New("registry unavailable")
	})
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if called || output.String() != version+"\n" {
		t.Fatalf("execute() called factory = %v, output = %q", called, output.String())
	}
}

func TestRunHelpPrintsUsageAndSucceeds(t *testing.T) {
	registry := source.NewRegistry(nil, source.Options{})
	var output bytes.Buffer

	if err := run(context.Background(), []string{"--help"}, &output, registry); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(output.String(), "Usage: agtlog") || !strings.Contains(output.String(), "--version") {
		t.Fatalf("run() help = %q, want usage and flags", output.String())
	}
}

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	registry := source.NewRegistry(nil, source.Options{})
	err := run(context.Background(), []string{"list"}, io.Discard, registry)
	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("run() error = %v, want unexpected argument", err)
	}
}

func TestExecuteWritesInvalidFlagUsageOnlyToDiagnostics(t *testing.T) {
	var output, diagnostics bytes.Buffer
	err := executeWithDiagnostics(context.Background(), []string{"--definitely-unknown"}, &output, &diagnostics, func() (*source.Registry, error) {
		return source.NewRegistry(nil, source.Options{}), nil
	})
	if err == nil {
		t.Fatal("executeWithDiagnostics() error = nil, want invalid flag")
	}
	if output.Len() != 0 || !strings.Contains(diagnostics.String(), "Usage: agtlog") {
		t.Fatalf("stdout = %q, diagnostics = %q", output.String(), diagnostics.String())
	}
}
