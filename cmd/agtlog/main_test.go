package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

func TestApplicationContextCancelsOnInterrupt(t *testing.T) {
	signal.Ignore(os.Interrupt)
	t.Cleanup(func() { signal.Reset(os.Interrupt) })
	ctx, stop := newApplicationContext()
	defer stop()

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("application context was not cancelled by interrupt")
	}
}

func TestDefaultCacheDirUsesXDGThenHome(t *testing.T) {
	tests := []struct {
		name string
		xdg  string
		want string
	}{
		{name: "xdg", xdg: "/cache", want: "/cache/agtlog"},
		{name: "home fallback", want: "/workspace/home/.cache/agtlog"},
		{name: "relative xdg fallback", xdg: "relative-cache", want: "/workspace/home/.cache/agtlog"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := defaultCacheDir("/workspace/home", test.xdg); got != test.want {
				t.Fatalf("defaultCacheDir() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConfiguredWatchRootCountUsesSelectedAgent(t *testing.T) {
	home := t.TempDir()
	claudeA := filepath.Join(home, "claude-a")
	claudeB := filepath.Join(home, "claude-b")
	missing := filepath.Join(home, "missing")
	codexHome := filepath.Join(home, "codex")
	for _, root := range []string{filepath.Join(claudeA, "projects"), filepath.Join(claudeB, "projects"), filepath.Join(codexHome, "sessions")} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	claudeConfig := strings.Join([]string{claudeA, missing, claudeB}, ",")
	if got := configuredWatchRootCount(home, claudeConfig, codexHome, ""); got != 3 {
		t.Fatalf("all-agent root count = %d, want 3", got)
	}
	if got := configuredWatchRootCount(home, claudeConfig, codexHome, "codex"); got != 1 {
		t.Fatalf("Codex root count = %d, want 1", got)
	}
	if got := configuredWatchRootCount(home, missing, missing, ""); got != 0 {
		t.Fatalf("missing root count = %d, want 0", got)
	}
}

func TestDefaultRegistryProtectsUnselectedAgentRootFromCache(t *testing.T) {
	home := t.TempDir()
	claudeRoot := filepath.Join(home, "claude")
	codexRoot := filepath.Join(home, "codex")
	xdgRoot := filepath.Join(home, "xdg")
	if err := os.MkdirAll(filepath.Join(claudeRoot, "projects", "observatory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(xdgRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","uuid":"user-1","timestamp":"2026-01-02T03:04:05Z","sessionId":"session-1","cwd":"/workspace/observatory","message":{"role":"user","content":"Map lunar craters"}}` + "\n"
	if err := os.WriteFile(filepath.Join(claudeRoot, "projects", "observatory", "session.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(codexRoot, filepath.Join(xdgRoot, "agtlog")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)
	t.Setenv("CODEX_HOME", codexRoot)
	t.Setenv("XDG_CACHE_HOME", xdgRoot)

	registry, err := defaultRegistry(context.Background(), cliOptions{agent: "claude", offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(codexRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unselected Codex root entries = %v, want none", entries)
	}
}

func TestDefaultRegistryRefreshFailsWhenCacheResolutionIsUnsafe(t *testing.T) {
	home := t.TempDir()
	codexRoot := filepath.Join(home, "codex")
	xdgRoot := filepath.Join(home, "xdg")
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(xdgRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(codexRoot, filepath.Join(xdgRoot, "agtlog")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("CODEX_HOME", codexRoot)
	t.Setenv("XDG_CACHE_HOME", xdgRoot)

	_, err := defaultRegistry(context.Background(), cliOptions{refreshPrices: true})
	if err == nil || !strings.Contains(err.Error(), "safely resolved outside agent") {
		t.Fatalf("defaultRegistry() error = %v, want unsafe cache refresh error", err)
	}
}

func TestDefaultRegistryRefreshRejectsCacheInsideConfigRoot(t *testing.T) {
	home := t.TempDir()
	codexRoot := filepath.Join(home, "codex")
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("CODEX_HOME", codexRoot)
	t.Setenv("XDG_CACHE_HOME", codexRoot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := defaultRegistry(ctx, cliOptions{refreshPrices: true})
	if err == nil || !strings.Contains(err.Error(), "safely resolved outside agent") {
		t.Fatalf("defaultRegistry() error = %v, want unsafe config-root cache error", err)
	}
	if _, err := os.Stat(filepath.Join(codexRoot, "agtlog")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache directory stat error = %v, want not exist", err)
	}
}

func TestResolveVersionUsesLinkerThenModuleMetadata(t *testing.T) {
	tests := []struct {
		name   string
		linked string
		module string
		want   string
	}{
		{name: "linked release", linked: "v1.2.3", module: "v1.2.2", want: "v1.2.3"},
		{name: "go install module", linked: "dev", module: "v1.2.3", want: "v1.2.3"},
		{name: "local build", linked: "dev", module: "(devel)", want: "dev"},
		{name: "missing metadata", linked: "dev", want: "dev"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveVersion(test.linked, test.module); got != test.want {
				t.Fatalf("resolveVersion() = %q, want %q", got, test.want)
			}
		})
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
	err := execute(context.Background(), []string{"--version", "--refresh-prices"}, &output, func(context.Context, cliOptions) (*source.Registry, error) {
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

func TestExecutePrintsHelpBeforeBuildingRegistry(t *testing.T) {
	var output bytes.Buffer
	called := false
	err := execute(context.Background(), []string{"--refresh-prices", "--help"}, &output, func(context.Context, cliOptions) (*source.Registry, error) {
		called = true
		return nil, errors.New("registry unavailable")
	})
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if called || !strings.Contains(output.String(), "--refresh-prices") {
		t.Fatalf("execute() called factory = %v, output = %q", called, output.String())
	}
}

func TestRunHelpPrintsUsageAndSucceeds(t *testing.T) {
	registry := source.NewRegistry(nil, source.Options{})
	var output bytes.Buffer

	if err := run(context.Background(), []string{"--help"}, &output, registry); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(output.String(), "Usage: agtlog") || !strings.Contains(output.String(), "--refresh-prices") || !strings.Contains(output.String(), "--version") || !strings.Contains(output.String(), "--theme default|nord|dracula") {
		t.Fatalf("run() help = %q, want usage and flags", output.String())
	}
	if !strings.Contains(output.String(), "--theme > AGTLOG_THEME > default; NO_COLOR forces mono") {
		t.Fatalf("run() help does not explain theme precedence: %q", output.String())
	}
}

func TestRunDispatchesListSubcommand(t *testing.T) {
	session := &model.Session{ID: "session-a", Agent: model.AgentClaude, Title: "Plan launch"}
	registry := source.NewRegistry([]source.Source{staticSource{session: session}}, source.Options{Workers: 1})
	var output bytes.Buffer
	if err := run(context.Background(), []string{"list"}, &output, registry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"command": "list"`) || !strings.Contains(output.String(), `"ref": "claude:session-a"`) {
		t.Fatalf("list output = %q", output.String())
	}
}

func TestParseOptionsReadsOfflineRefreshWatchAndAgentSelection(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want cliOptions
	}{
		{
			name: "existing options",
			args: []string{"--offline", "--no-watch", "--agent", "codex", "--theme", "nord"},
			want: cliOptions{offline: true, noWatch: true, agent: "codex", theme: "nord"},
		},
		{
			name: "refresh prices",
			args: []string{"--refresh-prices"},
			want: cliOptions{refreshPrices: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseOptions(test.args, io.Discard)
			if err != nil {
				t.Fatalf("parseOptions() error = %v", err)
			}
			if options != test.want {
				t.Fatalf("parseOptions() = %#v, want %#v", options, test.want)
			}
		})
	}
}

func TestExecuteApplicationReportsFlagErrorsOnce(t *testing.T) {
	var diagnostics bytes.Buffer
	err := executeApplication(
		context.Background(),
		[]string{"--agent"},
		strings.NewReader(""),
		io.Discard,
		&diagnostics,
		func(context.Context, cliOptions) (*source.Registry, error) {
			t.Fatal("registry factory called")
			return nil, nil
		},
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
			t.Fatal("TUI runner called")
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "flag needs an argument") {
		t.Fatalf("executeApplication() error = %v", err)
	}
	if got := diagnostics.String(); !strings.Contains(got, "Usage: agtlog") || strings.Contains(got, err.Error()) {
		t.Fatalf("diagnostics = %q, want usage without duplicated error", got)
	}
}

func TestExecuteApplicationRejectsUnknownThemeBeforeDiscovery(t *testing.T) {
	noColor, hadNoColor := os.LookupEnv("NO_COLOR")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadNoColor {
			_ = os.Setenv("NO_COLOR", noColor)
		}
	})
	called := false
	err := executeApplication(
		context.Background(),
		[]string{"--theme", "solarized"},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
		func(context.Context, cliOptions) (*source.Registry, error) {
			called = true
			return nil, nil
		},
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error { return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "unknown theme") || called {
		t.Fatalf("executeApplication() error = %v, factory called = %v", err, called)
	}
}

func TestExecuteApplicationPassesOptionsToRegistryFactory(t *testing.T) {
	type contextKey string
	appCtx := context.WithValue(context.Background(), contextKey("request"), "application")
	var receivedCtx context.Context
	var receivedOptions cliOptions
	factory := func(ctx context.Context, options cliOptions) (*source.Registry, error) {
		receivedCtx = ctx
		receivedOptions = options
		return source.NewRegistry(nil, source.Options{}), nil
	}
	runnerCalled := false
	runner := func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
		runnerCalled = true
		return nil
	}

	err := executeApplication(appCtx, []string{"--refresh-prices", "--no-watch", "--agent", "codex"}, strings.NewReader(""), io.Discard, io.Discard, factory, runner)
	if err != nil {
		t.Fatalf("executeApplication() error = %v", err)
	}
	wantOptions := cliOptions{refreshPrices: true, noWatch: true, agent: "codex"}
	if receivedCtx != appCtx || receivedOptions != wantOptions || !runnerCalled {
		t.Fatalf("factory context matches = %v, options = %#v, want %#v, runner called = %v", receivedCtx == appCtx, receivedOptions, wantOptions, runnerCalled)
	}
}

func TestExecuteApplicationStopsBeforeTUIWhenRefreshFails(t *testing.T) {
	refreshErr := errors.New("pricing refresh failed")
	var output, diagnostics bytes.Buffer

	err := executeApplication(
		context.Background(),
		[]string{"--refresh-prices"},
		strings.NewReader(""),
		&output,
		&diagnostics,
		func(_ context.Context, options cliOptions) (*source.Registry, error) {
			if !options.refreshPrices {
				t.Fatal("registry factory did not receive refresh option")
			}
			return nil, refreshErr
		},
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
			t.Fatal("TUI runner called after refresh failure")
			return nil
		},
	)
	if !errors.Is(err, refreshErr) {
		t.Fatalf("executeApplication() error = %v, want %v", err, refreshErr)
	}
	if output.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("stdout = %q, diagnostics = %q, want no pre-main output", output.String(), diagnostics.String())
	}
}

func TestExecuteApplicationPassesSelectedThemeToTUI(t *testing.T) {
	noColor, hadNoColor := os.LookupEnv("NO_COLOR")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadNoColor {
			_ = os.Setenv("NO_COLOR", noColor)
		}
	})
	factory := func(context.Context, cliOptions) (*source.Registry, error) {
		return source.NewRegistry(nil, source.Options{}), nil
	}
	runner := func(_ context.Context, _ io.Reader, _ io.Writer, initial tui.Model, _ <-chan source.SessionUpdate) error {
		if got := initial.ThemeName(); got != "nord" {
			t.Fatalf("TUI theme = %q, want nord", got)
		}
		return nil
	}

	if err := executeApplication(context.Background(), []string{"--no-watch", "--theme", "nord"}, strings.NewReader(""), io.Discard, io.Discard, factory, runner); err != nil {
		t.Fatal(err)
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

type quittingTeaModel struct{}

func (quittingTeaModel) Init() tea.Cmd                       { return tea.Quit }
func (quittingTeaModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return quittingTeaModel{}, nil }
func (quittingTeaModel) View() string                        { return "" }

func TestInteractiveProgramEnablesMouseCellMotion(t *testing.T) {
	var output bytes.Buffer
	program := newBubbleTeaProgram(context.Background(), nil, &output, quittingTeaModel{})
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	for _, sequences := range [][2]string{
		{"\x1b[?1002h", "\x1b[?1002l"},
		{"\x1b[?1006h", "\x1b[?1006l"},
	} {
		enabledAt := strings.Index(output.String(), sequences[0])
		disabledAt := strings.Index(output.String(), sequences[1])
		if enabledAt < 0 || disabledAt <= enabledAt {
			t.Fatalf("interactive output %q does not balance mouse sequences %q", output.String(), sequences)
		}
	}
	if strings.Contains(output.String(), "\x1b[?1003h") {
		t.Fatalf("interactive output %q enabled all-motion mouse reporting", output.String())
	}
}

func TestExecuteWritesInvalidFlagUsageOnlyToDiagnostics(t *testing.T) {
	var output, diagnostics bytes.Buffer
	err := executeWithDiagnostics(context.Background(), []string{"--definitely-unknown"}, &output, &diagnostics, func(context.Context, cliOptions) (*source.Registry, error) {
		return source.NewRegistry(nil, source.Options{}), nil
	})
	if err == nil {
		t.Fatal("executeWithDiagnostics() error = nil, want invalid flag")
	}
	if output.Len() != 0 || !strings.Contains(diagnostics.String(), "Usage: agtlog") {
		t.Fatalf("stdout = %q, diagnostics = %q", output.String(), diagnostics.String())
	}
}

func TestExecuteRejectsOfflineRefreshOnlyOnDiagnostics(t *testing.T) {
	var output, diagnostics bytes.Buffer
	called := false
	err := executeWithDiagnostics(context.Background(), []string{"--offline", "--refresh-prices"}, &output, &diagnostics, func(context.Context, cliOptions) (*source.Registry, error) {
		called = true
		return nil, errors.New("registry factory called")
	})
	if err == nil || !strings.Contains(err.Error(), "--offline") || !strings.Contains(err.Error(), "--refresh-prices") {
		t.Fatalf("executeWithDiagnostics() error = %v, want incompatible flags", err)
	}
	if called {
		t.Fatal("registry factory called for incompatible flags")
	}
	if output.Len() != 0 || !strings.Contains(diagnostics.String(), "Usage: agtlog") {
		t.Fatalf("stdout = %q, diagnostics = %q", output.String(), diagnostics.String())
	}
}
