package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

type fakeRegistry struct {
	sessions    []*model.Session
	diagnostics []source.DiscoveryDiagnostic
	load        func(*model.Session) error
}

func (registry *fakeRegistry) DiscoverWithDiagnostics(context.Context) ([]*model.Session, []source.DiscoveryDiagnostic, error) {
	return registry.sessions, registry.diagnostics, nil
}

func (registry *fakeRegistry) LoadDetail(_ context.Context, session *model.Session) error {
	if registry.load != nil {
		return registry.load(session)
	}
	return nil
}

func TestDirListAccumulatesValues(t *testing.T) {
	var dirs DirList
	if err := dirs.Set("home-a"); err != nil {
		t.Fatal(err)
	}
	if err := dirs.Set("home-b"); err != nil {
		t.Fatal(err)
	}
	if got, want := []string(dirs), []string{"home-a", "home-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DirList = %v, want %v", got, want)
	}
	if _, ok := any(&dirs).(interface{ IsBoolFlag() bool }); ok {
		t.Fatal("DirList implements IsBoolFlag")
	}
	var value flag.Value = &dirs
	if value.String() == "" {
		t.Fatal("DirList.String() is empty after Set")
	}
}

func TestParseDirListUsesPlatformSeparator(t *testing.T) {
	separator := string(os.PathListSeparator)
	got := ParseDirList(strings.Join([]string{" home-a ", "", "archive,home", "  "}, separator))
	want := []string{"home-a", "archive,home"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDirList() = %v, want %v", got, want)
	}
}

func TestResolveDirsPrefersExplicitValues(t *testing.T) {
	separator := string(os.PathListSeparator)
	if got, want := ResolveDirs([]string{"flag-home"}, "env-a"+separator+"env-b"), []string{"flag-home"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveDirs() = %v, want %v", got, want)
	}
	if got, want := ResolveDirs(nil, "env-a"+separator+"env-b"), []string{"env-a", "env-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveDirs() = %v, want %v", got, want)
	}
}

func TestValidateAgentDirs(t *testing.T) {
	home := t.TempDir()
	regularFile := filepath.Join(home, "agent-home.txt")
	if err := os.WriteFile(regularFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "missing-home")

	tests := []struct {
		name       string
		agent      string
		claudeDirs []string
		codexDirs  []string
		want       string
	}{
		{name: "missing Claude home", claudeDirs: []string{missing}, want: "claude directory does not exist: " + missing},
		{name: "Codex home is file", codexDirs: []string{regularFile}, want: "codex directory is not a directory: " + regularFile},
		{name: "fresh homes", claudeDirs: []string{home}, codexDirs: []string{home}},
		{name: "Claude filter ignores Codex", agent: "claude", claudeDirs: []string{home}, codexDirs: []string{missing}},
		{name: "Codex filter ignores Claude", agent: "codex", claudeDirs: []string{missing}, codexDirs: []string{home}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateAgentDirs(test.agent, test.claudeDirs, test.codexDirs)
			if test.want == "" && err != nil {
				t.Fatalf("ValidateAgentDirs() error = %v", err)
			}
			if test.want != "" && (err == nil || err.Error() != test.want) {
				t.Fatalf("ValidateAgentDirs() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCommonAgentDirectoryFlagsReachRegistryOptions(t *testing.T) {
	claudeHome := t.TempDir()
	codexHome := t.TempDir()
	var got Options
	err := Execute(context.Background(), []string{"list", "--claude-dir", claudeHome, "--claude-dir=" + claudeHome, "--codex-dir", codexHome}, io.Discard, io.Discard, func(_ context.Context, options Options) (Registry, error) {
		got = options
		return &fakeRegistry{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{claudeHome, claudeHome}; !reflect.DeepEqual(got.ClaudeDirs, want) {
		t.Fatalf("ClaudeDirs = %v, want %v", got.ClaudeDirs, want)
	}
	if want := []string{codexHome}; !reflect.DeepEqual(got.CodexDirs, want) {
		t.Fatalf("CodexDirs = %v, want %v", got.CodexDirs, want)
	}
}

func TestAgentDirectoryFlagsPreserveShowAndSearchOperands(t *testing.T) {
	home := t.TempDir()
	show, ref, err := parseShowOptions([]string{"--claude-dir", home, "session-alpha"}, io.Discard)
	if err != nil || ref != "session-alpha" || !reflect.DeepEqual([]string(show.common.claudeDirs), []string{home}) {
		t.Fatalf("parseShowOptions() = %#v, %q, %v", show, ref, err)
	}
	search, pattern, err := parseSearchOptions([]string{"lunar", "--codex-dir", home}, io.Discard)
	if err != nil || pattern != "lunar" || !reflect.DeepEqual([]string(search.common.codexDirs), []string{home}) {
		t.Fatalf("parseSearchOptions() = %#v, %q, %v", search, pattern, err)
	}
}

func TestParseTimeFilterAcceptsSupportedForms(t *testing.T) {
	location := time.FixedZone("fictional", 9*60*60)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, location)
	tests := []struct {
		value string
		want  time.Time
	}{
		{value: "2026-08-01T00:00:00-04:00", want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.FixedZone("", -4*60*60))},
		{value: "2026-08-01", want: time.Date(2026, 8, 1, 0, 0, 0, 0, location)},
		{value: "7d", want: now.Add(-7 * 24 * time.Hour)},
		{value: "24h", want: now.Add(-24 * time.Hour)},
		{value: "90m", want: now.Add(-90 * time.Minute)},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := parseTimeFilter(test.value, now, location)
			if err != nil || !got.Equal(test.want) || got.Format("-07:00") != test.want.Format("-07:00") {
				t.Fatalf("parseTimeFilter(%q) = %v, %v; want %v", test.value, got, err, test.want)
			}
		})
	}
	if _, err := parseTimeFilter("last-week", now, location); err == nil {
		t.Fatal("malformed time was accepted")
	}
}

func TestTokenTotalsNormalizeAgentInputSemantics(t *testing.T) {
	claude := tokenTotals(model.Usage{InputTokens: 60, OutputTokens: 5, CacheCreation5mTokens: 3, CacheCreation1hTokens: 2, CacheReadTokens: 40})
	codex := tokenTotals(model.Usage{InputTokens: 100, OutputTokens: 5, CacheCreation5mTokens: 3, CacheCreation1hTokens: 2, CacheReadTokens: 40, InputIncludesCacheRead: true})
	if !reflect.DeepEqual(claude, codex) {
		t.Fatalf("Claude totals %#v differ from Codex totals %#v", claude, codex)
	}
	if got := codex.UncachedInput + codex.Output + codex.CacheWrite + codex.CacheRead; got != codex.Total {
		t.Fatalf("disjoint categories sum to %d, want total %d", got, codex.Total)
	}
}

func TestListFiltersSortsAndPages(t *testing.T) {
	zone := time.FixedZone("fictional", 2*60*60)
	registry := &fakeRegistry{
		sessions: []*model.Session{
			{ID: "session-old", Agent: model.AgentCodex, Project: "forge", CWD: "/workspace/forge/branch", Title: "Map crater", UpdatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, zone), Messages: 2},
			{ID: "session-new", Agent: model.AgentClaude, Project: "forge", CWD: "/workspace/forge", Title: "Map lunar crater", UpdatedAt: time.Date(2026, 8, 4, 10, 0, 0, 0, zone), Messages: 8},
			{ID: "session-other", Agent: model.AgentClaude, Project: "harbor", CWD: "/workspace/harbor", Title: "Map crater", UpdatedAt: time.Date(2026, 8, 3, 10, 0, 0, 0, zone), Messages: 5},
		},
		diagnostics: []source.DiscoveryDiagnostic{{Agent: model.AgentCodex, Path: "/logs/broken.jsonl", Err: errors.New("invalid record")}},
	}
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"list", "--project", "forge", "--cwd", "/workspace/forge", "--query", "lunar", "--sort", "messages", "--order", "desc", "--limit", "1"}, &stdout, &stderr, func(_ context.Context, options Options) (Registry, error) {
		if !options.Offline {
			t.Fatal("subcommand did not force offline pricing")
		}
		return registry, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var response ListResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if stderr.Len() != 0 || len(response.Sessions) != 1 || response.Sessions[0].Ref != "claude:session-new" {
		t.Fatalf("response = %#v, stderr = %q", response, stderr.String())
	}
	if response.Page != (ListPage{Offset: 0, Limit: 1, Returned: 1, Total: 1, NextOffset: 1}) {
		t.Fatalf("page = %#v", response.Page)
	}
	if len(response.Warnings) != 1 || response.Warnings[0].Path != "/logs/broken.jsonl" {
		t.Fatalf("warnings = %#v", response.Warnings)
	}
}

func TestListPagingDoesNotOverflow(t *testing.T) {
	registry := &fakeRegistry{sessions: []*model.Session{{ID: "session-a", Agent: model.AgentClaude}}}
	var output bytes.Buffer
	maximumInt := int(^uint(0) >> 1)
	args := []string{"list", "--offset", strconv.Itoa(maximumInt), "--limit", strconv.Itoa(maximumInt)}
	if err := Execute(context.Background(), args, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response ListResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Page.Returned != 0 || response.Page.NextOffset != maximumInt {
		t.Fatalf("page = %#v", response.Page)
	}
}

func TestCWDFilterDoesNotTreatMissingCWDAsProcessDirectory(t *testing.T) {
	registry := &fakeRegistry{sessions: []*model.Session{{ID: "session-a", Agent: model.AgentClaude}}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"list", "--cwd", "."}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response ListResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sessions) != 0 {
		t.Fatalf("sessions = %#v", response.Sessions)
	}
}

func TestListRequiredZeroAndEmptyFieldsSurviveEncoding(t *testing.T) {
	registry := &fakeRegistry{sessions: []*model.Session{{ID: "zero-session", Agent: model.AgentClaude}}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"list"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"models": []`, `"has_error": false`, `"usd": 0`, `"estimated": false`, `"warnings": []`} {
		if !strings.Contains(output.String(), required) {
			t.Fatalf("output omitted %s:\n%s", required, output.String())
		}
	}
}

func TestListExcludesWorkflowGroupsFromSubagentCount(t *testing.T) {
	direct := &model.Session{ID: "direct-scout", Agent: model.AgentClaude}
	nested := &model.Session{ID: "nested-mapper", Agent: model.AgentClaude}
	group := &model.Session{ID: "wf-river-run", Agent: model.AgentClaude, Group: true, Subagents: []*model.Session{nested}}
	root := &model.Session{ID: "session-workflow", Agent: model.AgentClaude, Subagents: []*model.Session{direct, group}}
	registry := &fakeRegistry{sessions: []*model.Session{root}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"list"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response ListResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].Subagents != 2 {
		t.Fatalf("sessions = %#v, want two agents excluding workflow group", response.Sessions)
	}
}

func TestListDistinguishesUnaddressableSessionsFromUnreadableLogs(t *testing.T) {
	registry := &fakeRegistry{sessions: []*model.Session{{Agent: model.AgentClaude, Path: "/logs/session-without-id.jsonl"}}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"list"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response ListResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Warnings) != 1 || response.Warnings[0].Code != "unaddressable_session" || !strings.Contains(response.Warnings[0].Message, "could not address claude session") || strings.Contains(response.Warnings[0].Message, "could not read") {
		t.Fatalf("warnings = %#v", response.Warnings)
	}
}

func TestListRejectsInvalidFlagsAndLimits(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--limit", "0"},
		{"list", "--theme", "nord"},
		{"list", "--offline", "--refresh-prices"},
		{"list", "--format", "yaml"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := Execute(context.Background(), args, &stdout, &stderr, func(context.Context, Options) (Registry, error) {
				t.Fatal("factory called for usage error")
				return nil, nil
			})
			status, ok := ExitStatus(err)
			if !ok || status != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code": "usage"`) {
				t.Fatalf("error = %v, status = %d, stdout = %q, stderr = %q", err, status, stdout.String(), stderr.String())
			}
		})
	}
}

func TestSubcommandHelpWritesOnlyStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"list", "--help"}, &stdout, &stderr, func(context.Context, Options) (Registry, error) {
		t.Fatal("factory called for help")
		return nil, nil
	})
	if err != nil || !strings.Contains(stdout.String(), "Usage: agtlog list") || !strings.Contains(stdout.String(), "-limit") || !strings.Contains(stdout.String(), "overrides AGTLOG_CLAUDE_DIRS") || stderr.Len() != 0 {
		t.Fatalf("error = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
}
