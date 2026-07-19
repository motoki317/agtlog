package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/tui"
)

func TestTerminalFieldSanitizesDiagnostics(t *testing.T) {
	got := terminalField("safe\u202ereversed\r\n\x1bforged", 200)
	if strings.ContainsAny(got, "\r\n\x1b") || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("terminalField() emitted unsafe text %q", got)
	}
}

type staticSource struct {
	session *model.Session
}

type reconcilingSource struct {
	root      string
	discovers int
}

func (s *reconcilingSource) Agent() model.AgentKind { return model.AgentClaude }
func (s *reconcilingSource) Roots() []string        { return []string{s.root} }
func (s *reconcilingSource) Discover(context.Context) ([]string, error) {
	s.discovers++
	return []string{filepath.Join(s.root, "session.jsonl")}, nil
}
func (s *reconcilingSource) Parse(path string) (*model.Session, error) {
	title := "Initial"
	if s.discovers > 1 {
		title = "Reconciled"
	}
	return &model.Session{ID: "session-a", Agent: model.AgentClaude, Path: path, Title: title}, nil
}

func (s staticSource) Agent() model.AgentKind { return s.session.Agent }
func (s staticSource) Roots() []string        { return nil }
func (s staticSource) Discover(context.Context) ([]string, error) {
	return []string{"session.jsonl"}, nil
}
func (s staticSource) Parse(string) (*model.Session, error) { return s.session, nil }

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
	err := execute(context.Background(), []string{"--version"}, &output, func(string) (*source.Registry, error) {
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

func TestParseOptionsReadsWatchAndAgentSelection(t *testing.T) {
	options, err := parseOptions([]string{"--no-watch", "--agent", "codex"}, io.Discard)
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if !options.noWatch || options.agent != "codex" {
		t.Fatalf("parseOptions() = %#v", options)
	}
}

func TestExecuteApplicationPassesAgentToRegistryFactory(t *testing.T) {
	selected := ""
	factory := func(agent string) (*source.Registry, error) {
		selected = agent
		return source.NewRegistry(nil, source.Options{}), nil
	}
	runner := func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error { return nil }

	err := executeApplication(context.Background(), []string{"--no-watch", "--agent", "codex"}, strings.NewReader(""), io.Discard, io.Discard, factory, runner)
	if err != nil {
		t.Fatalf("executeApplication() error = %v", err)
	}
	if selected != "codex" {
		t.Fatalf("factory agent = %q, want codex", selected)
	}
}

func TestExecuteTUIUsesStaticSnapshotWithoutWatch(t *testing.T) {
	session := &model.Session{ID: "session-a", Agent: model.AgentClaude, Project: "starship", Title: "Plan the launch"}
	registry := source.NewRegistry([]source.Source{staticSource{session: session}}, source.Options{Workers: 1})
	called := false
	runner := func(_ context.Context, _ io.Reader, _ io.Writer, initial tui.Model, updates <-chan source.SessionUpdate) error {
		called = true
		if updates != nil {
			t.Fatal("static launch received live update channel")
		}
		if !strings.Contains(initial.View(), "Plan the launch") {
			t.Fatalf("initial view missing discovered session:\n%s", initial.View())
		}
		return nil
	}

	if err := executeTUI(context.Background(), cliOptions{noWatch: true}, strings.NewReader(""), io.Discard, registry, runner); err != nil {
		t.Fatalf("executeTUI() error = %v", err)
	}
	if !called {
		t.Fatal("runner was not called")
	}
}

func TestExecuteTUIReconcilesAfterWatcherStarts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &reconcilingSource{root: root}
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1})
	runner := func(_ context.Context, _ io.Reader, _ io.Writer, initial tui.Model, _ <-chan source.SessionUpdate) error {
		if !strings.Contains(initial.View(), "Reconciled") {
			t.Fatalf("initial view was not reconciled after watcher setup:\n%s", initial.View())
		}
		return nil
	}

	if err := executeTUI(context.Background(), cliOptions{}, strings.NewReader(""), io.Discard, registry, runner); err != nil {
		t.Fatalf("executeTUI() error = %v", err)
	}
}

func TestBubbleTeaRunnerDegradesToStaticFrameOffTerminal(t *testing.T) {
	session := &model.Session{ID: "session-a", Agent: model.AgentClaude, Title: "Plan the launch"}
	initial := tui.NewModel([]*model.Session{session}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var output bytes.Buffer

	if err := runBubbleTea(ctx, strings.NewReader(""), &output, initial, nil); err != nil {
		t.Fatalf("runBubbleTea() error = %v", err)
	}
	if !strings.Contains(output.String(), "Plan the launch") || strings.Contains(output.String(), "\x1b[?1049h") {
		t.Fatalf("non-terminal output = %q", output.String())
	}
}

func TestBubbleTeaRunnerPrintsEverySessionOffTerminal(t *testing.T) {
	sessions := make([]*model.Session, 20)
	for index := range sessions {
		sessions[index] = &model.Session{ID: fmt.Sprintf("session-%02d", index), Agent: model.AgentClaude, Title: fmt.Sprintf("Survey %02d", index)}
	}
	var output bytes.Buffer
	if err := runBubbleTea(context.Background(), strings.NewReader(""), &output, tui.NewModel(sessions, nil), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Survey 00") || !strings.Contains(output.String(), "Survey 19") {
		t.Fatalf("static output omitted sessions:\n%s", output.String())
	}
}

func TestExecuteWritesInvalidFlagUsageOnlyToDiagnostics(t *testing.T) {
	var output, diagnostics bytes.Buffer
	err := executeWithDiagnostics(context.Background(), []string{"--definitely-unknown"}, &output, &diagnostics, func(string) (*source.Registry, error) {
		return source.NewRegistry(nil, source.Options{}), nil
	})
	if err == nil {
		t.Fatal("executeWithDiagnostics() error = nil, want invalid flag")
	}
	if output.Len() != 0 || !strings.Contains(diagnostics.String(), "Usage: agtlog") {
		t.Fatalf("stdout = %q, diagnostics = %q", output.String(), diagnostics.String())
	}
}
