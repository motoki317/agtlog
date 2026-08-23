package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	machinecli "github.com/motoki317/agtlog/internal/cli"
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

func TestCapGOMAXPROCS(t *testing.T) {
	tests := []struct {
		name        string
		environment map[string]string
		current     int
		wantCalls   []int
	}{
		{name: "caps larger default", current: 24, wantCalls: []int{0, defaultMaxProcs}},
		{name: "keeps smaller default", current: 4, wantCalls: []int{0}},
		{name: "keeps explicit value", environment: map[string]string{"GOMAXPROCS": "24"}, current: 24},
		{name: "keeps explicit empty value", environment: map[string]string{"GOMAXPROCS": ""}, current: 24},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []int
			capGOMAXPROCS(func(name string) (string, bool) {
				value, ok := test.environment[name]
				return value, ok
			}, func(value int) int {
				calls = append(calls, value)
				return test.current
			})
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("GOMAXPROCS calls = %v, want %v", calls, test.wantCalls)
			}
		})
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

func writeCodexSession(t *testing.T, home, sessionID string) string {
	t.Helper()
	root := filepath.Join(home, "sessions", "2026", "08", "23")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "rollout-"+sessionID+".jsonl")
	content := fmt.Sprintf("{\"timestamp\":\"2026-08-23T01:02:03Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":%q,\"cwd\":\"/workspace/observatory\",\"originator\":\"codex_exec\"}}\n", sessionID) +
		"{\"timestamp\":\"2026-08-23T01:02:04Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"Map a lunar crater\"}}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCodexSidecarRun(t *testing.T, home string) {
	t.Helper()
	root := filepath.Join(home, "sessions", "2026", "08", "23")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := strings.Join([]string{
		`{"timestamp":"2037-08-23T01:02:03Z","type":"session_meta","payload":{"id":"aaaaaaaa-1111-2222-3333-444444444555","session_id":"aaaaaaaa-1111-2222-3333-444444444555","cwd":"/workspace/observatory","originator":"codex_exec"}}`,
		`{"timestamp":"2037-08-23T01:02:04Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2037-08-23T01:02:05Z","type":"event_msg","payload":{"type":"user_message","message":"Orbit survey"}}`,
		`{"timestamp":"2037-08-23T01:02:06Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":6000,"total_tokens":6000},"last_token_usage":{"input_tokens":6000,"total_tokens":6000}}}}`,
		`{"timestamp":"2037-08-23T01:02:07Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"bbbbbbbb-1111-2222-3333-444444444666","agent_path":"/root/scout","kind":"started"}}`,
	}, "\n") + "\n"
	child := strings.Join([]string{
		`{"timestamp":"2037-08-23T01:02:07Z","type":"session_meta","payload":{"id":"bbbbbbbb-1111-2222-3333-444444444666","session_id":"aaaaaaaa-1111-2222-3333-444444444555","parent_thread_id":"aaaaaaaa-1111-2222-3333-444444444555","cwd":"/workspace/observatory","thread_source":"subagent","agent_path":"/root/scout"}}`,
		`{"timestamp":"2037-08-23T01:02:08Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}`,
		`{"timestamp":"2037-08-23T01:02:09Z","type":"event_msg","payload":{"type":"user_message","message":"Scout mirror"}}`,
		`{"timestamp":"2037-08-23T01:02:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2000,"total_tokens":2000},"last_token_usage":{"input_tokens":2000,"total_tokens":2000}}}}`,
	}, "\n") + "\n"
	files := map[string]string{
		"rollout-aaaaaaaa-1111-2222-3333-444444444555.jsonl": parent,
		"rollout-bbbbbbbb-1111-2222-3333-444444444666.jsonl": child,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeClaudeReplay(t *testing.T, home, project, sessionID, timestamp string) string {
	t.Helper()
	root := filepath.Join(home, "projects", project)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, sessionID+".jsonl")
	content := fmt.Sprintf("{\"type\":\"user\",\"uuid\":\"user-%s\",\"timestamp\":%q,\"sessionId\":%q,\"cwd\":\"/workspace/observatory\",\"message\":{\"role\":\"user\",\"content\":\"Map a lunar crater\"}}\n", sessionID, timestamp, sessionID) +
		fmt.Sprintf("{\"type\":\"assistant\",\"uuid\":\"assistant-%s\",\"timestamp\":%q,\"sessionId\":%q,\"cwd\":\"/workspace/observatory\",\"requestId\":\"request-shared\",\"message\":{\"id\":\"message-shared\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n", sessionID, timestamp, sessionID)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setRegistryEnvironment(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-base"))
	t.Setenv("CODEX_HOME", "")
	t.Setenv("AGTLOG_CLAUDE_DIRS", "")
	t.Setenv("AGTLOG_CODEX_DIRS", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot = append(snapshot, relative+string(filepath.Separator))
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot = append(snapshot, relative+"\x00"+string(content))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestDefaultRegistryDiscoversDefaultAndExtraCodexHomes(t *testing.T) {
	home := t.TempDir()
	setRegistryEnvironment(t, home)
	writeCodexSession(t, filepath.Join(home, ".codex"), "session-default")
	extraHome := filepath.Join(home, "codex-archive")
	writeCodexSession(t, extraHome, "session-archive")
	extraBefore := snapshotTree(t, extraHome)

	registry, err := defaultRegistry(context.Background(), cliOptions{agent: "codex", offline: true, codexDirs: []string{extraHome}})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, session := range sessions {
		ids[session.ID] = true
	}
	if len(sessions) != 2 || !ids["session-default"] || !ids["session-archive"] {
		t.Fatalf("sessions = %#v, want default and archive", sessions)
	}

	var output bytes.Buffer
	if err := executeApplication(
		context.Background(), []string{"list", "--agent", "codex", "--codex-dir", extraHome},
		strings.NewReader(""), &output, io.Discard, defaultRegistry,
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
			t.Fatal("TUI runner called for list command")
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "session-default") || !strings.Contains(output.String(), "session-archive") {
		t.Fatalf("list output = %q, want default and archive sessions", output.String())
	}
	if extraAfter := snapshotTree(t, extraHome); !reflect.DeepEqual(extraAfter, extraBefore) {
		t.Fatalf("configured Codex home changed from %q to %q", extraBefore, extraAfter)
	}
}

func TestDefaultRegistryResolvesFlagsBeforeEnvironmentPerAgent(t *testing.T) {
	home := t.TempDir()
	setRegistryEnvironment(t, home)
	claudeBase := filepath.Join(home, "claude-base")
	codexBase := filepath.Join(home, "codex-base")
	claudeFlag := filepath.Join(home, "claude-flag")
	claudeEnv := filepath.Join(home, "claude-env")
	codexEnv := filepath.Join(home, "codex-env")
	for _, dir := range []string{claudeBase, codexBase, claudeFlag, claudeEnv, codexEnv} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeBase)
	t.Setenv("CODEX_HOME", codexBase)
	t.Setenv("AGTLOG_CLAUDE_DIRS", claudeEnv)
	t.Setenv("AGTLOG_CODEX_DIRS", codexEnv)

	registry, err := defaultRegistry(context.Background(), cliOptions{offline: true, claudeDirs: []string{claudeFlag}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(claudeBase, "projects"),
		filepath.Join(claudeFlag, "projects"),
		filepath.Join(codexBase, "sessions"),
		filepath.Join(codexEnv, "sessions"),
	}
	if got := registry.Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry roots = %v, want %v", got, want)
	}
}

func TestDefaultRegistryReadsPlatformSeparatedEnvironmentHomes(t *testing.T) {
	home := t.TempDir()
	setRegistryEnvironment(t, home)
	first := filepath.Join(home, "codex,archive")
	second := filepath.Join(home, "codex-second")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AGTLOG_CODEX_DIRS", strings.Join([]string{" " + first + " ", "", second}, string(os.PathListSeparator)))

	registry, err := defaultRegistry(context.Background(), cliOptions{agent: "codex", offline: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(first, "sessions"),
		filepath.Join(second, "sessions"),
	}
	if got := registry.Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry roots = %v, want %v", got, want)
	}
}

func TestDefaultRegistryCollapsesMirroredClaudeHomes(t *testing.T) {
	home := t.TempDir()
	setRegistryEnvironment(t, home)
	first := filepath.Join(home, "claude-archive-a")
	second := filepath.Join(home, "claude-archive-b")
	original := writeClaudeReplay(t, first, "observatory", "session-mirror", "2026-08-23T01:02:03Z")
	mirrorRoot := filepath.Join(second, "projects", "observatory")
	if err := os.MkdirAll(mirrorRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mirrorRoot, "session-mirror.jsonl"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := defaultRegistry(context.Background(), cliOptions{agent: "claude", offline: true, claudeDirs: []string{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inputTokens := int64(0)
	for _, session := range sessions {
		inputTokens += session.OwnedUsage().InputTokens
	}
	if len(sessions) != 1 || inputTokens != 10 {
		t.Fatalf("sessions = %d, input tokens = %d, want one session and 10 tokens", len(sessions), inputTokens)
	}

	var output bytes.Buffer
	if err := executeApplication(
		context.Background(),
		[]string{"list", "--offline", "--agent", "claude", "--claude-dir", first, "--claude-dir", second, "--all"},
		strings.NewReader(""), &output, io.Discard, defaultRegistry,
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
			t.Fatal("TUI runner called for list command")
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	var response machinecli.ListResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].Tokens.UncachedInput != 10 || len(response.Warnings) != 0 {
		t.Fatalf("list response = %#v, want one owned mirrored session without warnings", response)
	}
}

func TestMirroredCodexSidecarsStayOneGraphAcrossFrontends(t *testing.T) {
	home := t.TempDir()
	setRegistryEnvironment(t, home)
	first := filepath.Join(home, "codex-archive-a")
	second := filepath.Join(home, "codex-archive-z")
	writeCodexSidecarRun(t, first)
	writeCodexSidecarRun(t, second)

	discover := func(dirs []string) ([]*model.Session, string) {
		t.Helper()
		registry, err := defaultRegistry(context.Background(), cliOptions{agent: "codex", offline: true, codexDirs: dirs})
		if err != nil {
			t.Fatal(err)
		}
		var sessions []*model.Session
		var view string
		runner := func(_ context.Context, _ io.Reader, _ io.Writer, initial tui.Model, updates <-chan source.SessionUpdate) error {
			select {
			case update := <-updates:
				if update.DiscoveryErr != nil || !update.DiscoveryComplete {
					t.Fatalf("TUI discovery update = %#v", update)
				}
				sessions = update.Sessions
				updated, _ := initial.Update(update)
				view = updated.(tui.Model).View()
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for TUI discovery")
			}
			return nil
		}
		if err := executeTUI(context.Background(), cliOptions{noWatch: true}, strings.NewReader(""), io.Discard, registry, runner); err != nil {
			t.Fatal(err)
		}
		return sessions, view
	}
	list := func(dirs []string) machinecli.ListResponse {
		t.Helper()
		args := []string{"list", "--format", "json", "--offline", "--agent", "codex", "--all"}
		for _, dir := range dirs {
			args = append(args, "--codex-dir", dir)
		}
		var output bytes.Buffer
		if err := executeApplication(
			context.Background(), args, strings.NewReader(""), &output, io.Discard, defaultRegistry,
			func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
				t.Fatal("TUI runner called for list command")
				return nil
			},
		); err != nil {
			t.Fatal(err)
		}
		var response machinecli.ListResponse
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	assertGraph := func(label string, sessions []*model.Session) {
		t.Helper()
		if len(sessions) != 1 || sessions[0].ID != "aaaaaaaa-1111-2222-3333-444444444555" ||
			len(sessions[0].Subagents) != 1 || sessions[0].Subagents[0].ID != "bbbbbbbb-1111-2222-3333-444444444666" {
			t.Fatalf("%s sessions = %#v, want one parent with one linked sidecar", label, sessions)
		}
		if got := sessions[0].OwnedUsage().TotalTokens(); got != 8000 {
			t.Fatalf("%s total tokens = %d, want 8000", label, got)
		}
		if got := sessions[0].OwnedCost().USD; got <= 0 {
			t.Fatalf("%s total cost = %f, want a priced parent and child", label, got)
		}
	}

	oneTUISessions, oneTUIView := discover([]string{first})
	mirroredTUISessions, mirroredTUIView := discover([]string{first, second})
	assertGraph("one-home TUI", oneTUISessions)
	assertGraph("mirrored TUI", mirroredTUISessions)
	if oneTUISessions[0].OwnedCost().USD != mirroredTUISessions[0].OwnedCost().USD {
		t.Fatalf("mirrored TUI cost = %f, want one-home cost %f", mirroredTUISessions[0].OwnedCost().USD, oneTUISessions[0].OwnedCost().USD)
	}
	for label, view := range map[string]string{"one-home TUI": oneTUIView, "mirrored TUI": mirroredTUIView} {
		if !strings.Contains(view, "1 sessions") || !strings.Contains(view, "Orbit survey") || strings.Contains(view, "Scout mirror") {
			t.Fatalf("%s view does not show one parent-only row:\n%s", label, view)
		}
	}
	if mirroredTUIView != oneTUIView {
		t.Fatalf("mirrored TUI view differs from one-home baseline:\n%s\n--- baseline ---\n%s", mirroredTUIView, oneTUIView)
	}

	oneList := list([]string{first})
	mirroredList := list([]string{first, second})
	if !reflect.DeepEqual(mirroredList, oneList) {
		t.Fatalf("mirrored list = %#v, want one-home response %#v", mirroredList, oneList)
	}
	if len(mirroredList.Sessions) != 1 || mirroredList.Page.Total != 1 || mirroredList.Sessions[0].Subagents != 1 ||
		mirroredList.Sessions[0].Tokens.Total != 8000 || mirroredList.Sessions[0].Cost.USD <= 0 || len(mirroredList.Warnings) != 0 {
		t.Fatalf("mirrored list response = %#v, want one complete parent graph", mirroredList)
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

func TestDefaultRegistryConfiguredRootKeepsCacheSafety(t *testing.T) {
	home := t.TempDir()
	setRegistryEnvironment(t, home)
	xdgRoot := filepath.Join(home, "cache-parent")
	configuredHome := filepath.Join(xdgRoot, "agtlog")
	if err := os.MkdirAll(configuredHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", xdgRoot)

	_, err := defaultRegistry(context.Background(), cliOptions{refreshPrices: true, codexDirs: []string{configuredHome}})
	if err == nil || !strings.Contains(err.Error(), "safely resolved outside agent") {
		t.Fatalf("defaultRegistry() error = %v, want unsafe configured-root cache error", err)
	}
	if entries, readErr := os.ReadDir(configuredHome); readErr != nil || len(entries) != 0 {
		t.Fatalf("configured home entries = %v, error = %v, want no writes", entries, readErr)
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
	session    *model.Session
	roots      []string
	rootsCalls *int
}

type blockingParseSource struct {
	parseStarted chan struct{}
	releaseParse chan struct{}
	parseDone    chan struct{}
}

func (s blockingParseSource) Agent() model.AgentKind   { return model.AgentClaude }
func (s blockingParseSource) CacheFingerprint() string { return "test-blocking-parser-v1" }
func (s blockingParseSource) Roots() []string          { return nil }
func (s blockingParseSource) Discover(context.Context) ([]string, error) {
	return []string{"session.jsonl"}, nil
}
func (s blockingParseSource) Parse(path string) (*model.Session, error) {
	close(s.parseStarted)
	defer close(s.parseDone)
	<-s.releaseParse
	return &model.Session{ID: "session-a", Agent: model.AgentClaude, Path: path}, nil
}
func (s blockingParseSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}
func (s blockingParseSource) Reprice(*model.Session) {}

type reconcilingSource struct {
	root             string
	discovers        int
	discoveryStarted chan struct{}
	releaseDiscovery chan struct{}
}

func (s *reconcilingSource) Agent() model.AgentKind   { return model.AgentClaude }
func (s *reconcilingSource) CacheFingerprint() string { return "test-reconciling-parser-v1" }
func (s *reconcilingSource) Roots() []string          { return []string{s.root} }
func (s *reconcilingSource) Discover(ctx context.Context) ([]string, error) {
	s.discovers++
	close(s.discoveryStarted)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.releaseDiscovery:
	}
	return []string{filepath.Join(s.root, "session.jsonl")}, nil
}
func (s *reconcilingSource) Parse(path string) (*model.Session, error) {
	id, title := "session-a", "Initial"
	if filepath.Base(path) == "watched.jsonl" {
		id, title = "session-watched", "Watched"
	}
	return &model.Session{ID: id, Agent: model.AgentClaude, Path: path, Title: title}, nil
}
func (s *reconcilingSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}
func (s *reconcilingSource) Reprice(*model.Session) {}

func (s staticSource) Agent() model.AgentKind   { return s.session.Agent }
func (s staticSource) CacheFingerprint() string { return "test-static-parser-v1" }
func (s staticSource) Roots() []string {
	if s.rootsCalls != nil {
		*s.rootsCalls++
	}
	return append([]string(nil), s.roots...)
}
func (s staticSource) Discover(context.Context) ([]string, error) {
	return []string{"session.jsonl"}, nil
}
func (s staticSource) Parse(path string) (*model.Session, error) {
	session := *s.session
	session.Path = path
	return &session, nil
}
func (s staticSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}
func (s staticSource) Reprice(*model.Session) {}

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
	if !strings.Contains(output.String(), "overrides AGTLOG_CLAUDE_DIRS") || !strings.Contains(output.String(), "same list separator as PATH") {
		t.Fatalf("run() help does not explain directory environment precedence: %q", output.String())
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
			if !reflect.DeepEqual(options, test.want) {
				t.Fatalf("parseOptions() = %#v, want %#v", options, test.want)
			}
		})
	}
}

func TestParseOptionsAccumulatesAgentDirectories(t *testing.T) {
	t.Setenv("AGTLOG_CLAUDE_DIRS", "")
	t.Setenv("AGTLOG_CODEX_DIRS", "")
	claudeA := t.TempDir()
	claudeB := t.TempDir()
	codexHome := t.TempDir()
	options, err := parseOptions([]string{
		"--claude-dir", claudeA,
		"--claude-dir=" + claudeB,
		"--codex-dir", codexHome,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(options.claudeDirs, []string{claudeA, claudeB}) || !reflect.DeepEqual(options.codexDirs, []string{codexHome}) {
		t.Fatalf("parseOptions() directories = %v, %v", options.claudeDirs, options.codexDirs)
	}
}

func TestParseOptionsFlagsBeatInvalidDirectoryEnvironment(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "missing")
	t.Setenv("AGTLOG_CLAUDE_DIRS", missing)
	t.Setenv("AGTLOG_CODEX_DIRS", missing)
	if _, err := parseOptions([]string{"--claude-dir", home, "--codex-dir", home}, io.Discard); err != nil {
		t.Fatalf("parseOptions() error = %v, want flags to override environment", err)
	}
}

func TestExecuteApplicationReportsMissingMachineDirectoryAsUsage(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "missing-claude")
	t.Setenv("AGTLOG_CLAUDE_DIRS", "")
	t.Setenv("AGTLOG_CODEX_DIRS", "")
	var output, diagnostics bytes.Buffer
	called := false
	err := executeApplication(
		context.Background(), []string{"list", "--claude-dir", missing}, strings.NewReader(""),
		&output, &diagnostics,
		func(context.Context, cliOptions) (*source.Registry, error) {
			called = true
			return nil, nil
		},
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error { return nil },
	)
	status, ok := machinecli.ExitStatus(err)
	if !ok || status != 2 || called || output.Len() != 0 || !strings.Contains(diagnostics.String(), `"code": "usage"`) || !strings.Contains(diagnostics.String(), "claude directory does not exist: "+missing) || strings.Contains(diagnostics.String(), `"code": "internal"`) {
		t.Fatalf("error = %v, status = %d, called = %v, stdout = %q, stderr = %q", err, status, called, output.String(), diagnostics.String())
	}
}

func TestExecuteApplicationValidatesDirectoryEnvironmentAsUsage(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "missing-codex")
	t.Setenv("AGTLOG_CLAUDE_DIRS", "")
	t.Setenv("AGTLOG_CODEX_DIRS", missing)
	var diagnostics bytes.Buffer
	err := executeApplication(
		context.Background(), []string{"list", "--agent", "codex"}, strings.NewReader(""), io.Discard, &diagnostics,
		func(context.Context, cliOptions) (*source.Registry, error) {
			t.Fatal("registry factory called")
			return nil, nil
		},
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error { return nil },
	)
	status, ok := machinecli.ExitStatus(err)
	if !ok || status != 2 || !strings.Contains(diagnostics.String(), "codex directory does not exist: "+missing) {
		t.Fatalf("error = %v, status = %d, stderr = %q", err, status, diagnostics.String())
	}
}

func TestExecuteApplicationRejectsMissingTUIDirectoryBeforeFactory(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "missing-claude")
	t.Setenv("AGTLOG_CLAUDE_DIRS", "")
	t.Setenv("AGTLOG_CODEX_DIRS", "")
	var diagnostics bytes.Buffer
	called := false
	err := executeApplication(
		context.Background(), []string{"--claude-dir", missing}, strings.NewReader(""), io.Discard, &diagnostics,
		func(context.Context, cliOptions) (*source.Registry, error) {
			called = true
			return nil, nil
		},
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error { return nil },
	)
	if err == nil || err.Error() != "claude directory does not exist: "+missing || called || !strings.Contains(diagnostics.String(), "Usage: agtlog") {
		t.Fatalf("error = %v, called = %v, diagnostics = %q", err, called, diagnostics.String())
	}
}

func TestExecuteApplicationRejectsMissingTUIEnvironmentDirectoryBeforeFactory(t *testing.T) {
	home := t.TempDir()
	missing := filepath.Join(home, "missing-codex")
	t.Setenv("AGTLOG_CLAUDE_DIRS", "")
	t.Setenv("AGTLOG_CODEX_DIRS", missing)
	var diagnostics bytes.Buffer
	called := false
	err := executeApplication(
		context.Background(), nil, strings.NewReader(""), io.Discard, &diagnostics,
		func(context.Context, cliOptions) (*source.Registry, error) {
			called = true
			return nil, nil
		},
		func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
			called = true
			return nil
		},
	)
	if err == nil || err.Error() != "codex directory does not exist: "+missing || called || !strings.Contains(diagnostics.String(), "Usage: agtlog") {
		t.Fatalf("error = %v, called = %v, diagnostics = %q", err, called, diagnostics.String())
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
	claudeHome := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("AGTLOG_CLAUDE_DIRS", "")
	t.Setenv("AGTLOG_CODEX_DIRS", "")

	err := executeApplication(appCtx, []string{
		"--refresh-prices", "--no-watch", "--agent", "codex",
		"--claude-dir", claudeHome, "--codex-dir", codexHome,
	}, strings.NewReader(""), io.Discard, io.Discard, factory, runner)
	if err != nil {
		t.Fatalf("executeApplication() error = %v", err)
	}
	wantOptions := cliOptions{refreshPrices: true, noWatch: true, agent: "codex", claudeDirs: []string{claudeHome}, codexDirs: []string{codexHome}}
	if receivedCtx != appCtx || !reflect.DeepEqual(receivedOptions, wantOptions) || !runnerCalled {
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

func TestExecuteTUIDiscoversAfterStartingWithoutWatch(t *testing.T) {
	session := &model.Session{ID: "session-a", Agent: model.AgentClaude, Project: "starship", Title: "Plan the launch"}
	rootsCalls := 0
	registry := source.NewRegistry([]source.Source{staticSource{session: session, rootsCalls: &rootsCalls}}, source.Options{Workers: 1})
	rootsCalls = 0
	called := false
	runner := func(_ context.Context, _ io.Reader, _ io.Writer, initial tui.Model, updates <-chan source.SessionUpdate) error {
		called = true
		if updates == nil {
			t.Fatal("static launch received no discovery update channel")
		}
		if !initial.DiscoveryInFlight() || strings.Contains(initial.View(), "Plan the launch") || !strings.Contains(initial.View(), "Loading sessions") {
			t.Fatalf("initial view was not a loading frame:\n%s", initial.View())
		}
		select {
		case update := <-updates:
			updated, _ := initial.Update(update)
			initial = updated.(tui.Model)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for discovery")
		}
		if initial.DiscoveryInFlight() || !strings.Contains(initial.View(), "Plan the launch") {
			t.Fatalf("discovered session did not settle the model:\n%s", initial.View())
		}
		return nil
	}

	if err := executeTUI(context.Background(), cliOptions{noWatch: true}, strings.NewReader(""), io.Discard, registry, runner); err != nil {
		t.Fatalf("executeTUI() error = %v", err)
	}
	if !called {
		t.Fatal("runner was not called")
	}
	if rootsCalls != 0 {
		t.Fatalf("--no-watch called source Roots %d times after registry construction", rootsCalls)
	}
}

func TestExecuteTUIWatchedRootCountUsesFilteredRegistryRoots(t *testing.T) {
	claudeRoot := filepath.Join(t.TempDir(), "claude-projects")
	codexRoot := filepath.Join(t.TempDir(), "codex-sessions")
	for _, root := range []string{claudeRoot, codexRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name    string
		sources []source.Source
		want    string
	}{
		{
			name: "all configured roots",
			sources: []source.Source{
				staticSource{session: &model.Session{ID: "claude-session", Agent: model.AgentClaude}, roots: []string{claudeRoot}},
				staticSource{session: &model.Session{ID: "codex-session", Agent: model.AgentCodex}, roots: []string{codexRoot}},
			},
			want: "watching 2 roots",
		},
		{
			name: "Claude filter excludes Codex root",
			sources: []source.Source{
				staticSource{session: &model.Session{ID: "claude-session", Agent: model.AgentClaude}, roots: []string{claudeRoot}},
			},
			want: "watching 1 root",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := source.NewRegistry(test.sources, source.Options{Workers: 1})
			runner := func(_ context.Context, _ io.Reader, _ io.Writer, initial tui.Model, _ <-chan source.SessionUpdate) error {
				if view := initial.View(); !strings.Contains(view, test.want) {
					t.Fatalf("initial view = %q, want %q", view, test.want)
				}
				return nil
			}
			if err := executeTUI(context.Background(), cliOptions{}, strings.NewReader(""), io.Discard, registry, runner); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecuteTUIQuitDoesNotWaitForInFlightParse(t *testing.T) {
	for _, noWatch := range []bool{true, false} {
		t.Run(fmt.Sprintf("no-watch-%t", noWatch), func(t *testing.T) {
			adapter := blockingParseSource{
				parseStarted: make(chan struct{}), releaseParse: make(chan struct{}), parseDone: make(chan struct{}),
			}
			registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1})
			runner := func(context.Context, io.Reader, io.Writer, tui.Model, <-chan source.SessionUpdate) error {
				select {
				case <-adapter.parseStarted:
					return nil
				case <-time.After(time.Second):
					return errors.New("parse did not start")
				}
			}
			done := make(chan error, 1)
			go func() {
				done <- executeTUI(context.Background(), cliOptions{noWatch: noWatch}, strings.NewReader(""), io.Discard, registry, runner)
			}()

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("executeTUI() error = %v", err)
				}
			case <-time.After(time.Second):
				close(adapter.releaseParse)
				<-done
				t.Fatal("quit waited for in-flight parse")
			}
			close(adapter.releaseParse)
			select {
			case <-adapter.parseDone:
			case <-time.After(time.Second):
				t.Fatal("released parse did not exit")
			}
		})
	}
}

func TestReceiveDiscoveryResultReturnsWhenFollowerClosureRacesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := make(chan discoveryResult)
	defer close(results)
	done := make(chan bool, 1)
	go func() {
		_, received := receiveDiscoveryResult(ctx, results)
		done <- received
	}()

	select {
	case received := <-done:
		if received {
			t.Fatal("receiveDiscoveryResult() waited for a discovery result after cancellation")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("receiveDiscoveryResult() blocked after cancellation")
	}
}

func TestExecuteTUIStartsWatcherBeforeSingleDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &reconcilingSource{
		root: root, discoveryStarted: make(chan struct{}), releaseDiscovery: make(chan struct{}),
	}
	release := func() {
		select {
		case <-adapter.releaseDiscovery:
		default:
			close(adapter.releaseDiscovery)
		}
	}
	defer release()
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1})
	runner := func(_ context.Context, _ io.Reader, _ io.Writer, initial tui.Model, updates <-chan source.SessionUpdate) error {
		if !initial.DiscoveryInFlight() {
			t.Fatalf("initial model was not loading:\n%s", initial.View())
		}
		select {
		case <-adapter.discoveryStarted:
		case <-time.After(time.Second):
			t.Fatal("discovery did not start")
		}
		if err := os.WriteFile(filepath.Join(root, "watched.jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		release()
		discoveryComplete, watcherUpdate := false, false
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		for !discoveryComplete || !watcherUpdate {
			select {
			case update, ok := <-updates:
				if !ok {
					t.Fatal("updates closed before discovery and watcher results arrived")
				}
				discoveryComplete = discoveryComplete || update.DiscoveryComplete
				for _, session := range update.Sessions {
					watcherUpdate = watcherUpdate || session.Title == "Watched"
				}
				updated, _ := initial.Update(update)
				initial = updated.(tui.Model)
			case <-deadline.C:
				t.Fatal("timed out waiting for discovery and watcher update")
			}
		}
		if !strings.Contains(initial.View(), "Initial") || !strings.Contains(initial.View(), "Watched") {
			t.Fatalf("discovery and watcher result =\n%s", initial.View())
		}
		return nil
	}

	if err := executeTUI(context.Background(), cliOptions{}, strings.NewReader(""), io.Discard, registry, runner); err != nil {
		t.Fatalf("executeTUI() error = %v", err)
	}
	if adapter.discovers != 1 {
		t.Fatalf("Discover() calls = %d, want 1", adapter.discovers)
	}
}

func TestExecuteTUINonTerminalWaitsForDiscovery(t *testing.T) {
	session := &model.Session{ID: "session-a", Agent: model.AgentClaude, Title: "Plan the launch"}
	registry := source.NewRegistry([]source.Source{staticSource{session: session}}, source.Options{Workers: 1})
	var output bytes.Buffer

	if err := executeTUI(context.Background(), cliOptions{noWatch: true}, strings.NewReader(""), &output, registry, runBubbleTea); err != nil {
		t.Fatalf("executeTUI() error = %v", err)
	}
	if !strings.Contains(output.String(), "Plan the launch") || strings.Contains(output.String(), "Loading sessions") {
		t.Fatalf("non-terminal output did not wait for discovery:\n%s", output.String())
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
