package source_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/codex"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

func TestReadRecordReportsChangedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	original := []byte(`{"message":"original"}`)
	if err := os.WriteFile(path, append(original, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := model.RecordRef{Path: path, Length: int64(len(original)), Digest: sha256.Sum256(original)}
	changed := []byte(`{"message":"modified"}`)
	if len(changed) != len(original) {
		t.Fatal("test records must have equal lengths")
	}
	if err := os.WriteFile(path, append(changed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := source.ReadRecord(context.Background(), ref)
	if !errors.Is(err, source.ErrRecordChanged) {
		t.Fatalf("ReadRecord() error = %v, want ErrRecordChanged", err)
	}
}

func TestReadRecordRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.jsonl")
	raw := []byte(`{"message":"source"}`)
	if err := os.WriteFile(target, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "session.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ref := model.RecordRef{Path: link, Length: int64(len(raw)), Digest: sha256.Sum256(raw)}

	_, err := source.ReadRecord(context.Background(), ref)
	if !errors.Is(err, source.ErrRecordRead) {
		t.Fatalf("ReadRecord() error = %v, want ErrRecordRead", err)
	}
}

func TestReadRecordRejectsInvalidReferencesBeforeReading(t *testing.T) {
	tests := []struct {
		name string
		ref  model.RecordRef
	}{
		{name: "empty path", ref: model.RecordRef{Length: 1}},
		{name: "negative offset", ref: model.RecordRef{Path: "/fictional/session.jsonl", Offset: -1, Length: 1}},
		{name: "zero length", ref: model.RecordRef{Path: "/fictional/session.jsonl"}},
		{name: "over line ceiling", ref: model.RecordRef{Path: "/fictional/session.jsonl", Length: jsonl.MaxLineBytes + 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := source.ReadRecord(context.Background(), test.ref)
			if !errors.Is(err, source.ErrRecordRead) {
				t.Fatalf("ReadRecord() error = %v, want ErrRecordRead", err)
			}
		})
	}
}

func TestReadRecordHonorsCancellationBeforeOpening(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := source.ReadRecord(ctx, model.RecordRef{Path: "/fictional/session.jsonl", Length: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadRecord() error = %v, want context.Canceled", err)
	}
}

func TestReadRecordSurvivesAppendAfterReferencedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	raw := []byte(`{"message":"original"}`)
	if err := os.WriteFile(path, append(append([]byte(nil), raw...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := model.RecordRef{Path: path, Length: int64(len(raw)), Digest: sha256.Sum256(raw)}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"message\":\"later\"}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := source.ReadRecord(context.Background(), ref)
	if err != nil {
		t.Fatalf("ReadRecord() error = %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("ReadRecord() = %q, want %q", got, raw)
	}
}

func TestRegistryDiscoversEveryRegisteredSource(t *testing.T) {
	cacheRead := 0.5
	calculator := cost.NewCalculator(cost.Table{
		"claude-opus-4-8": {Input: 1, Output: 1},
		"claude-fable-5":  {Input: 1, Output: 1},
		"claude-sonnet-5": {Input: 1, Output: 1},
		"gpt-5.6":         {Input: 1, Output: 1, CacheRead: &cacheRead},
		"gpt-5.4":         {Input: 1, Output: 1, CacheRead: &cacheRead},
		"gpt-5":           {Input: 1, Output: 1, CacheRead: &cacheRead},
	})
	sources := []source.Source{
		claude.NewSource(claude.NewParser(calculator), []string{filepath.Join("claude", "testdata")}),
		codex.NewSource(codex.NewParser(calculator, "gpt-5"), []string{filepath.Join("codex", "testdata", "sessions")}),
	}
	registry := source.NewRegistry(sources, source.Options{Workers: 2, CacheDir: t.TempDir()})

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	counts := map[model.AgentKind]int{}
	for _, session := range sessions {
		counts[session.Agent]++
	}
	if len(sessions) != 3 || counts[model.AgentClaude] != 1 || counts[model.AgentCodex] != 2 {
		t.Fatalf("Discover() returned %d sessions with counts %v", len(sessions), counts)
	}
}

func TestRegistryReportsPerPathDiagnostics(t *testing.T) {
	adapter := diagnosticSource{
		sessions: map[string]*model.Session{
			"good.jsonl": {ID: "session-good", Agent: model.AgentClaude, Path: "good.jsonl"},
		},
		errors: map[string]error{
			"broken-b.jsonl": errors.New("bad record b"),
			"broken-a.jsonl": errors.New("bad record a"),
		},
	}
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 3})

	sessions, diagnostics, err := registry.DiscoverWithDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-good" {
		t.Fatalf("sessions = %#v, want session-good", sessions)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, want two", diagnostics)
	}
	if diagnostics[0].Agent != model.AgentClaude || diagnostics[0].Path != "broken-a.jsonl" || diagnostics[0].Err.Error() != "bad record a" {
		t.Fatalf("first diagnostic = %#v", diagnostics[0])
	}
	if diagnostics[1].Path != "broken-b.jsonl" || diagnostics[1].Err.Error() != "bad record b" {
		t.Fatalf("second diagnostic = %#v", diagnostics[1])
	}

	legacySessions, err := registry.Discover(context.Background())
	if err != nil || len(legacySessions) != 1 {
		t.Fatalf("Discover() = %#v, %v", legacySessions, err)
	}
}

func TestRegistryRejectsParsedSessionAtDifferentPath(t *testing.T) {
	adapter := diagnosticSource{
		sessions: map[string]*model.Session{
			"discovered.jsonl": {ID: "session-spoofed", Agent: model.AgentClaude, Path: "other.jsonl"},
		},
	}
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1})

	sessions, diagnostics, err := registry.DiscoverWithDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != "discovered.jsonl" ||
		diagnostics[0].Err.Error() != "parsed session path does not match discovered path" {
		t.Fatalf("DiscoverWithDiagnostics() = sessions %#v, diagnostics %#v", sessions, diagnostics)
	}
}

func TestRegistryReportsDiscoveryProgress(t *testing.T) {
	adapter := diagnosticSource{
		sessions: map[string]*model.Session{
			"one.jsonl": {ID: "session-one", Agent: model.AgentClaude},
			"two.jsonl": {ID: "session-two", Agent: model.AgentClaude},
		},
		errors: map[string]error{
			"broken.jsonl": errors.New("bad record"),
		},
	}
	type progress struct {
		completed int
		total     int
	}
	var updates []progress
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{
		Workers: 3,
		Progress: func(completed, total int) {
			updates = append(updates, progress{completed: completed, total: total})
		},
	})

	if _, _, err := registry.DiscoverWithDiagnostics(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []progress{
		{completed: 0, total: 3},
		{completed: 1, total: 3},
		{completed: 2, total: 3},
		{completed: 3, total: 3},
	}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("progress updates = %#v, want %#v", updates, want)
	}
}

func TestRegistryReportsClaudeSubagentDiagnosticsFromCache(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project-orbit")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(project, "session-root.jsonl")
	parent := `{"type":"user","timestamp":"2026-01-02T00:00:00Z","sessionId":"session-root","cwd":"/workspace/orbit","message":{"content":"Inspect relay"}}` + "\n"
	if err := os.WriteFile(parentPath, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenPath := filepath.Join(project, "session-root", "subagents", "agent-broken.jsonl")
	if err := os.MkdirAll(brokenPath, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedBrokenPath := filepath.Join(project, "session-root", "subagents", "workflows", "wf-river-run", "agent-nested-broken.jsonl")
	if err := os.MkdirAll(nestedBrokenPath, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := claude.NewSource(claude.NewParser(cost.NewCalculator(cost.Table{})), []string{root})
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: t.TempDir()})
	for attempt := range 2 {
		sessions, diagnostics, err := registry.DiscoverWithDiagnostics(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 1 || len(diagnostics) != 2 ||
			!strings.HasSuffix(diagnostics[0].Path, filepath.Join("project-orbit", "session-root", "subagents", "agent-broken.jsonl")) ||
			!strings.HasSuffix(diagnostics[1].Path, filepath.Join("project-orbit", "session-root", "subagents", "workflows", "wf-river-run", "agent-nested-broken.jsonl")) {
			t.Fatalf("attempt %d: sessions = %#v, diagnostics = %#v", attempt, sessions, diagnostics)
		}
	}
}

type diagnosticSource struct {
	sessions map[string]*model.Session
	errors   map[string]error
}

type parseCountingSource struct {
	source.Source
	parses int
	paths  []string
}

func (s *parseCountingSource) Discover(ctx context.Context) ([]string, error) {
	if s.paths == nil {
		return s.Source.Discover(ctx)
	}
	return append([]string(nil), s.paths...), nil
}

func (s *parseCountingSource) ParseContext(ctx context.Context, path string) (*model.Session, error) {
	s.parses++
	return s.Source.ParseContext(ctx, path)
}

func (s diagnosticSource) Agent() model.AgentKind   { return model.AgentClaude }
func (s diagnosticSource) CacheFingerprint() string { return "test-diagnostic-parser-v1" }
func (s diagnosticSource) Roots() []string          { return nil }
func (s diagnosticSource) Discover(context.Context) ([]string, error) {
	paths := make([]string, 0, len(s.sessions)+len(s.errors))
	for path := range s.sessions {
		paths = append(paths, path)
	}
	for path := range s.errors {
		paths = append(paths, path)
	}
	return paths, nil
}
func (s diagnosticSource) Parse(path string) (*model.Session, error) {
	if err := s.errors[path]; err != nil {
		return nil, err
	}
	session := *s.sessions[path]
	return &session, nil
}
func (s diagnosticSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}
func (s diagnosticSource) Reprice(*model.Session) {}

func TestRegistryLinksCodexSubagentSidecarUsage(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "rollout-thread-root.jsonl")
	childPath := filepath.Join(root, "rollout-thread-review.jsonl")
	parent := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00Z","type":"session_meta","payload":{"id":"thread-root","session_id":"thread-root","cwd":"/workspace/lunar-lab"}}`,
		`{"timestamp":"2026-01-02T03:00:01Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"thread-review","agent_path":"/root/review_x","kind":"started"}}`,
	}, "\n") + "\n"
	child := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:00:00Z","type":"session_meta","payload":{"id":"thread-review","session_id":"thread-root","parent_thread_id":"thread-root","cwd":"/workspace/lunar-lab","thread_source":"subagent","agent_path":"/root/review_x"}}`,
		`{"timestamp":"2026-01-02T03:00:00.100Z","type":"session_meta","payload":{"id":"thread-root","session_id":"thread-root","cwd":"/workspace/parent-lab","thread_source":"user","agent_path":null}}`,
		`{"timestamp":"2026-01-02T03:00:00.200Z","type":"turn_context","payload":{"model":"gpt-5.6"}}`,
		`{"timestamp":"2026-01-02T03:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Review the lunar telemetry."}}`,
		`{"timestamp":"2026-01-02T03:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":120,"output_tokens":8,"total_tokens":128}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(parentPath, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	calculator := cost.NewCalculator(cost.Table{"gpt-5.6": {Input: 1, Output: 1}})
	adapter := codex.NewSource(codex.NewParser(calculator, "gpt-5.6"), []string{root})
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: t.TempDir()})

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || len(sessions[0].Subagents) != 1 {
		t.Fatalf("Discover() sessions = %#v, want one parent with one linked child", sessions)
	}
	childSession := sessions[0].Subagents[0]
	if childSession.ID != "thread-review" || childSession.ParentID != "thread-root" || childSession.Title != "review_x" {
		t.Fatalf("linked child identity = ID %q, parent %q, title %q", childSession.ID, childSession.ParentID, childSession.Title)
	}
	if got := childSession.TotalUsage().TotalTokens(); got != 128 {
		t.Fatalf("linked child usage = %d tokens, want 128", got)
	}
}

func TestRegistryInvalidatesClaudeCacheForSubagentChange(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project-alpha")
	subagents := filepath.Join(project, "session-main", "subagents")
	if err := os.MkdirAll(subagents, 0o700); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(project, "session-main.jsonl")
	mainLine := "{\"type\":\"user\",\"timestamp\":\"2026-01-02T00:00:00Z\",\"sessionId\":\"session-main\",\"cwd\":\"/workspace/starship\",\"message\":{\"content\":\"Plan\"}}\n"
	if err := os.WriteFile(mainPath, []byte(mainLine), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentPath := filepath.Join(subagents, "agent-scout.jsonl")
	first := "{\"type\":\"assistant\",\"timestamp\":\"2026-01-02T00:00:01Z\",\"agentId\":\"scout\",\"requestId\":\"request-a\",\"message\":{\"id\":\"message-a\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":10}}}\n"
	if err := os.WriteFile(subagentPath, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	calculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: 1}})
	adapter := claude.NewSource(claude.NewParser(calculator), []string{root})
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: t.TempDir()})

	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := "{\"type\":\"assistant\",\"timestamp\":\"2026-01-02T00:00:02Z\",\"agentId\":\"scout\",\"requestId\":\"request-b\",\"message\":{\"id\":\"message-b\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":20}}}\n"
	file, err := os.OpenFile(subagentPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(second); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions[0].TotalUsage().InputTokens; got != 30 {
		t.Fatalf("TotalUsage().InputTokens = %d, want 30 after subagent append", got)
	}
}

func TestRegistryRepricesCachedSummaryWhenPricingChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-main.jsonl")
	line := `{"type":"assistant","timestamp":"2026-01-02T00:00:00Z","sessionId":"session-main","requestId":"request-a","message":{"id":"message-a","model":"claude-opus-4-8","usage":{"input_tokens":10}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	discoverCost := func(inputRate float64) (float64, int) {
		calculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: inputRate}})
		concrete := claude.NewSource(claude.NewParser(calculator), []string{root})
		adapter := &parseCountingSource{Source: concrete}
		registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: cacheDir})
		sessions, err := registry.Discover(context.Background())
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		return sessions[0].Cost.USD, adapter.parses
	}

	if got, parses := discoverCost(1); got != 10 || parses != 1 {
		t.Fatalf("first discovery = cost %v, parses %d, want 10 and 1", got, parses)
	}
	if got, parses := discoverCost(2); got != 20 || parses != 0 {
		t.Fatalf("priced cache hit = cost %v, parses %d, want 20 and 0", got, parses)
	}
}

func TestRegistryCachedPricingMatchesColdNestedClaudeParse(t *testing.T) {
	calculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: 2, Output: 3}})
	path := filepath.Join("claude", "testdata", "workflow", "subagents", "session-workflow.jsonl")
	concrete := claude.NewSource(claude.NewParser(calculator), nil)
	adapter := &parseCountingSource{Source: concrete, paths: []string{path}}
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: t.TempDir()})

	cold, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := sessionPricingViewOf(cold[0])
	warm, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionPricingViewOf(warm[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("cached pricing = %#v, want cold parse %#v", got, want)
	}
	if adapter.parses != 1 || !hasNestedPricingGroup(warm[0]) {
		t.Fatalf("round trip = %d parses, graph %#v, want one parse and nested subagents", adapter.parses, warm[0])
	}
}

func TestRegistryCachedCodexPricingKeepsRequestTiers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-tiered.jsonl")
	lines := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"turn_context","payload":{"model":"gpt-5.6"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150000},"last_token_usage":{"input_tokens":150000}}}}`,
		`{"timestamp":"2026-01-02T03:06:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300000},"last_token_usage":{"input_tokens":150000}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	above := 2.0
	calculator := cost.NewCalculator(cost.Table{"gpt-5.6": {Input: 1, InputAbove272K: &above}})
	concrete := codex.NewSource(codex.NewParser(calculator, "gpt-5"), []string{root})
	adapter := &parseCountingSource{Source: concrete}
	registry := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: t.TempDir()})

	if _, err := registry.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session := sessions[0]
	if session.Cost.USD != 300_000 || len(session.Requests) != 2 ||
		session.Requests[0].USD != 150_000 || session.Requests[1].USD != 150_000 {
		t.Fatalf("cached Codex pricing = cost %#v, requests %#v, want separate base tiers", session.Cost, session.Requests)
	}
	if adapter.parses != 1 {
		t.Fatalf("ParseContext() called %d times, want one cold parse", adapter.parses)
	}
}

func TestRegistryRepricesCachedCodexSummaryWhenFallbackChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-fallback.jsonl")
	lines := strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05Z","type":"turn_context","payload":{"model":"future-codex-model"}}`,
		`{"timestamp":"2026-01-02T03:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10},"last_token_usage":{"input_tokens":10}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	discover := func(defaultModel string, inputRate float64) (*model.Session, int) {
		calculator := cost.NewCalculator(cost.Table{defaultModel: {Input: inputRate}})
		concrete := codex.NewSource(codex.NewParser(calculator, defaultModel), []string{root})
		adapter := &parseCountingSource{Source: concrete}
		sessions, err := source.NewRegistry([]source.Source{adapter}, source.Options{Workers: 1, CacheDir: cacheDir}).Discover(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return sessions[0], adapter.parses
	}

	if session, parses := discover("fallback-a", 1); session.Cost.USD != 10 || parses != 1 {
		t.Fatalf("first discovery = cost %#v, parses %d, want $10 and one parse", session.Cost, parses)
	}
	session, parses := discover("fallback-b", 3)
	wantRates := []model.EstimatedRate{{Model: "future-codex-model", PricingModel: "fallback-b"}}
	if session.Cost.USD != 30 || parses != 0 || !reflect.DeepEqual(session.Cost.EstimatedRates, wantRates) {
		t.Fatalf("fallback cache hit = cost %#v, parses %d, want $30, zero parses, rates %#v", session.Cost, parses, wantRates)
	}
}

func TestRegistryCachedOwnershipUsesRepricedRequests(t *testing.T) {
	root := t.TempDir()
	logs := map[string]string{
		"session-origin.jsonl": `{"type":"assistant","timestamp":"2026-01-02T00:00:00Z","sessionId":"session-origin","requestId":"request-shared","message":{"id":"message-shared","model":"claude-opus-4-8","usage":{"input_tokens":10}}}` + "\n",
		"session-replay.jsonl": `{"type":"assistant","timestamp":"2026-01-02T00:01:00Z","sessionId":"session-replay","requestId":"request-shared","message":{"id":"message-shared","model":"claude-opus-4-8","usage":{"input_tokens":10}}}` + "\n",
	}
	for name, data := range logs {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cacheDir := t.TempDir()
	oldCalculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: 1}})
	oldSource := claude.NewSource(claude.NewParser(oldCalculator), []string{root})
	oldAdapter := &parseCountingSource{Source: oldSource}
	if _, err := source.NewRegistry([]source.Source{oldAdapter}, source.Options{Workers: 1, CacheDir: cacheDir}).Discover(context.Background()); err != nil {
		t.Fatal(err)
	}

	newCalculator := cost.NewCalculator(cost.Table{"claude-opus-4-8": {Input: 2}})
	newSource := claude.NewSource(claude.NewParser(newCalculator), []string{root})
	fresh, err := source.NewRegistry([]source.Source{newSource}, source.Options{Workers: 1}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cachedAdapter := &parseCountingSource{Source: newSource}
	cached, err := source.NewRegistry([]source.Source{cachedAdapter}, source.Options{Workers: 1, CacheDir: cacheDir}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cachedAdapter.parses != 0 {
		t.Fatalf("cached pricing parsed %d files, want none", cachedAdapter.parses)
	}
	freshByID := sessionsByID(fresh)
	for _, session := range cached {
		want := freshByID[session.ID]
		if want == nil || session.DuplicatedUSD != want.DuplicatedUSD || !reflect.DeepEqual(session.OwnedCost(), want.OwnedCost()) {
			t.Fatalf("cached ownership for %q = duplicated %v, owned %#v; want %v and %#v", session.ID, session.DuplicatedUSD, session.OwnedCost(), want.DuplicatedUSD, want.OwnedCost())
		}
		if session.OwnedCost().USD < 0 {
			t.Fatalf("cached owned cost for %q = %v, want nonnegative", session.ID, session.OwnedCost().USD)
		}
	}
	if replay := sessionsByID(cached)["session-replay"]; replay.DuplicatedUSD != 20 || replay.OwnedCost().USD != 0 {
		t.Fatalf("cached replay = %#v, want $20 fully duplicated", replay)
	}
}

type sessionPricingView struct {
	Cost                model.Cost
	ModelCosts          map[string]float64
	ModelCostBreakdowns map[string]model.CostBreakdown
	RequestUSD          []float64
	Subagents           []sessionPricingView
}

func sessionPricingViewOf(session *model.Session) sessionPricingView {
	view := sessionPricingView{
		Cost: session.Cost, ModelCosts: session.ModelCosts, ModelCostBreakdowns: session.ModelCostBreakdowns,
		RequestUSD: make([]float64, len(session.Requests)), Subagents: make([]sessionPricingView, len(session.Subagents)),
	}
	for index := range session.Requests {
		view.RequestUSD[index] = session.Requests[index].USD
	}
	for index, subagent := range session.Subagents {
		view.Subagents[index] = sessionPricingViewOf(subagent)
	}
	return view
}

func sessionsByID(sessions []*model.Session) map[string]*model.Session {
	result := make(map[string]*model.Session, len(sessions))
	for _, session := range sessions {
		result[session.ID] = session
	}
	return result
}

func hasNestedPricingGroup(session *model.Session) bool {
	if session.Group && len(session.Subagents) > 0 && len(session.ModelCosts) > 0 {
		return true
	}
	for _, subagent := range session.Subagents {
		if hasNestedPricingGroup(subagent) {
			return true
		}
	}
	return false
}

func TestRegistryLoadsDetailThroughBothAdapters(t *testing.T) {
	cacheRead := 0.5
	calculator := cost.NewCalculator(cost.Table{
		"claude-opus-4-8": {Input: 1, Output: 1},
		"claude-fable-5":  {Input: 1, Output: 1},
		"claude-sonnet-5": {Input: 1, Output: 1},
		"gpt-5.6":         {Input: 1, Output: 1, CacheRead: &cacheRead},
		"gpt-5.4":         {Input: 1, Output: 1, CacheRead: &cacheRead},
		"gpt-5":           {Input: 1, Output: 1, CacheRead: &cacheRead},
	})
	registry := source.NewRegistry([]source.Source{
		claude.NewSource(claude.NewParser(calculator), []string{filepath.Join("claude", "testdata")}),
		codex.NewSource(codex.NewParser(calculator, "gpt-5"), []string{filepath.Join("codex", "testdata", "sessions")}),
	}, source.Options{Workers: 2})
	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if len(session.Events) != 0 {
			t.Fatalf("Discover() eagerly populated %s events", session.Agent)
		}
	}
	loaded := map[model.AgentKind]bool{}
	for _, session := range sessions {
		if loaded[session.Agent] {
			continue
		}
		if err := registry.LoadDetail(context.Background(), session); err != nil {
			t.Fatalf("LoadDetail(%s) error = %v", session.Agent, err)
		}
		if len(session.Events) == 0 {
			t.Fatalf("LoadDetail(%s) produced no events", session.Agent)
		}
		loaded[session.Agent] = true
	}
	if !loaded[model.AgentClaude] || !loaded[model.AgentCodex] {
		t.Fatalf("loaded agents = %v", loaded)
	}
}
