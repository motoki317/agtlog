package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

func TestShowRejectsRawWithoutEvents(t *testing.T) {
	var stderr bytes.Buffer
	err := Execute(context.Background(), []string{"show", "session-a", "--raw", "0", "--no-events"}, io.Discard, &stderr, func(context.Context, Options) (Registry, error) {
		t.Fatal("factory called for conflicting flags")
		return nil, nil
	})
	if errorCode(err) != "usage" || !strings.Contains(stderr.String(), "cannot be used together") {
		t.Fatalf("error = %v, stderr = %q", err, stderr.String())
	}
}

func TestShowRejectsRawTextFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"show", "session-a", "--raw", "0", "--format", "text"}, &stdout, &stderr, func(context.Context, Options) (Registry, error) {
		t.Fatal("factory called for incompatible raw format")
		return nil, nil
	})
	if exitCode(err) != 2 || errorCode(err) != "usage" || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--raw requires --format json") {
		t.Fatalf("error = %#v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
}

func TestShowNoEventsDoesNotDescribeCachedTimeline(t *testing.T) {
	session := &model.Session{
		ID:     "session-a",
		Agent:  model.AgentClaude,
		Events: []model.Event{{Kind: model.EventUser, Text: "cached detail"}},
	}
	registry := &fakeRegistry{sessions: []*model.Session{session}, load: func(*model.Session) error {
		t.Fatal("--no-events loaded detail")
		return nil
	}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"show", "session-a", "--no-events", "--offset", "3"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response ShowResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := ShowPage{Offset: 3, NextOffset: 3, Complete: true}
	if len(response.Events) != 0 || response.Page != want {
		t.Fatalf("events = %#v, page = %#v; want empty events and %#v", response.Events, response.Page, want)
	}
}

func TestShowRejectsEventKindsOutsideWireVersion(t *testing.T) {
	registry := &fakeRegistry{sessions: []*model.Session{{ID: "session-a", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventKind("future-kind")}}}}}
	var stderr bytes.Buffer
	err := Execute(context.Background(), []string{"show", "session-a"}, io.Discard, &stderr, func(context.Context, Options) (Registry, error) { return registry, nil })
	if errorCode(err) != "internal" || !strings.Contains(stderr.String(), "no schema v1 mapping") {
		t.Fatalf("error = %v, stderr = %q", err, stderr.String())
	}
}

func TestResolveSelectorAcceptsEverySelectorForm(t *testing.T) {
	child := &model.Session{ID: "thread-review-789", Agent: model.AgentCodex, AgentPath: "/root/review_x", Path: "/logs/root-a.jsonl#review_x"}
	root := &model.Session{ID: "session-alpha-123", Agent: model.AgentCodex, Path: "/logs/root-a.jsonl", Subagents: []*model.Session{child}}
	nodes := indexSessionGraphs([]*model.Session{root})
	tests := []struct {
		name     string
		selector string
		wantRef  string
	}{
		{name: "canonical root", selector: "codex:session-alpha-123", wantRef: "codex:session-alpha-123"},
		{name: "canonical child", selector: "codex:session-alpha-123#review_x", wantRef: "codex:session-alpha-123#review_x"},
		{name: "bare id", selector: "session-alpha-123", wantRef: "codex:session-alpha-123"},
		{name: "unique prefix", selector: "session-a", wantRef: "codex:session-alpha-123"},
		{name: "thread id", selector: "thread-review-789", wantRef: "codex:session-alpha-123#review_x"},
		{name: "absolute path", selector: "/logs/root-a.jsonl", wantRef: "codex:session-alpha-123"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveSelector(test.selector, nodes, nil)
			if err != nil || got.ref != test.wantRef {
				t.Fatalf("resolveSelector(%q) = %q, %v; want %q", test.selector, got.ref, err, test.wantRef)
			}
		})
	}
}

func TestResolveSelectorRejectsShortUnknownAndAmbiguousPrefixes(t *testing.T) {
	roots := []*model.Session{
		{ID: "abcdef-one", Agent: model.AgentCodex, Project: "forge", Title: "One", UpdatedAt: time.Unix(2, 0)},
		{ID: "abcdef-two", Agent: model.AgentClaude, Project: "forge", Title: "Two", UpdatedAt: time.Unix(1, 0)},
	}
	nodes := indexSessionGraphs(roots)
	if _, err := resolveSelector("abc", nodes, nil); exitCode(err) != 2 {
		t.Fatalf("short prefix error = %v, want usage", err)
	}
	if _, err := resolveSelector("unknown", nodes, nil); errorCode(err) != "not_found" || exitCode(err) != 3 {
		t.Fatalf("unknown selector error = %#v", err)
	}
	_, err := resolveSelector("abcdef", nodes, nil)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Status != 3 || exit.Detail.Code != "ambiguous_ref" || len(exit.Detail.Candidates) != 2 {
		t.Fatalf("ambiguous selector error = %#v", err)
	}
	if exit.Detail.Candidates[0].Ref != "claude:abcdef-two" || exit.Detail.Candidates[1].Ref != "codex:abcdef-one" {
		t.Fatalf("candidates = %#v, want canonical sort", exit.Detail.Candidates)
	}
	if selected, exactErr := resolveSelector("abcdef-one", nodes, nil); exactErr != nil || selected.ref != "codex:abcdef-one" {
		t.Fatalf("exact selector = %#v, %v", selected, exactErr)
	}
}

func TestCanonicalRefSurvivesInlineChildThreadUpgrade(t *testing.T) {
	child := &model.Session{ID: "review_x", Agent: model.AgentCodex, AgentPath: "/root/review_x", Path: "/logs/root.jsonl#review_x"}
	root := &model.Session{ID: "thread-root", Agent: model.AgentCodex, Subagents: []*model.Session{child}}
	before := indexSessionGraphs([]*model.Session{root})[1].ref
	child.ID = "thread-child"
	after := indexSessionGraphs([]*model.Session{root})[1].ref
	if before != "codex:thread-root#review_x" || after != before {
		t.Fatalf("canonical ref changed from %q to %q", before, after)
	}
}

func TestCanonicalRefsEscapeDelimitersFromLogs(t *testing.T) {
	child := &model.Session{ID: "review#one", Agent: model.AgentClaude, AgentPath: "/root/review#one", Path: "/logs/root.jsonl#review#one"}
	root := &model.Session{ID: "root#review", Agent: model.AgentClaude, Path: "/logs/root.jsonl", Subagents: []*model.Session{child}}
	nodes := indexSessionGraphs([]*model.Session{root})
	if len(nodes) != 2 || nodes[0].ref != "claude:root%23review" || nodes[1].ref != "claude:root%23review#review%23one" {
		t.Fatalf("refs = %#v", nodes)
	}
	selected, err := resolveSelector(nodes[1].ref, nodes, nil)
	if err != nil || selected.session != child {
		t.Fatalf("resolveSelector() = %#v, %v", selected, err)
	}
	if _, err := resolveSelector("review#one", nodes, nil); errorCode(err) != "not_found" {
		t.Fatalf("bare inline selector error = %#v", err)
	}
}

func TestAddressableRootsRejectMissingAndDuplicateIdentities(t *testing.T) {
	missing := &model.Session{Agent: model.AgentClaude, Path: "/logs/missing.jsonl"}
	first := &model.Session{ID: "duplicate", Agent: model.AgentClaude, Path: "/logs/first.jsonl"}
	second := &model.Session{ID: "duplicate", Agent: model.AgentClaude, Path: "/logs/second.jsonl"}
	roots, diagnostics := addressableRoots([]*model.Session{missing, first, second}, nil)
	if len(roots) != 0 || len(diagnostics) != 3 {
		t.Fatalf("roots = %#v, diagnostics = %#v", roots, diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.code != "unaddressable_session" {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
	}
}

func TestShowPathReportsUnaddressableSessionAccurately(t *testing.T) {
	path := "/logs/session-without-id.jsonl"
	registry := &fakeRegistry{sessions: []*model.Session{{Agent: model.AgentClaude, Path: path}}}
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"show", path}, &stdout, &stderr, func(context.Context, Options) (Registry, error) { return registry, nil })
	if exitCode(err) != 1 || errorCode(err) != "unaddressable_session" || stdout.Len() != 0 || !strings.Contains(stderr.String(), "selected session cannot be addressed") || strings.Contains(stderr.String(), "could not be read") {
		t.Fatalf("error = %#v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
}

func TestShowOperandTolerantFlagsAndFilteredPagination(t *testing.T) {
	zone := time.FixedZone("fictional", 3*60*60)
	session := &model.Session{
		ID: "session-show", Agent: model.AgentClaude, UpdatedAt: time.Now(),
		Events: []model.Event{
			{Timestamp: time.Date(2026, 8, 1, 1, 0, 0, 0, zone), Kind: model.EventUser, Text: "first"},
			{Timestamp: time.Date(2026, 8, 1, 1, 1, 0, 0, zone), Kind: model.EventToolCall, ToolName: "Read", ToolInput: "ridge"},
			{Timestamp: time.Date(2026, 8, 1, 1, 2, 0, 0, zone), Kind: model.EventAssistantText, Text: "middle"},
			{Timestamp: time.Date(2026, 8, 1, 1, 3, 0, 0, zone), Kind: model.EventToolCall, ToolName: "Edit", ToolInput: "map"},
		},
	}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	var output bytes.Buffer
	err := Execute(context.Background(), []string{"show", "session-show", "--offset", "1", "--kind", "tool-call", "--limit", "1"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil })
	if err != nil {
		t.Fatal(err)
	}
	var response ShowResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Events) != 1 || response.Events[0].Index != 1 || response.Events[0].Text != "Read(ridge)" {
		t.Fatalf("events = %#v", response.Events)
	}
	if response.Page.NextOffset != 2 || !response.Page.HasMore || response.Page.Complete {
		t.Fatalf("page = %#v", response.Page)
	}
}

func TestShowBoundsEveryTextFieldAndReportsTruncation(t *testing.T) {
	event := model.Event{
		Kind: model.EventToolCall, ToolName: "Edit", ToolInput: "preview",
		ResultSummary: "summary", Detail: &model.ToolDetail{Input: "abcdef", Diff: "ghijkl", Output: "mnopqr"},
	}
	result := eventDTO(0, event, nil, 3)
	if result.Text != "E…)" || result.Tool.Input != "a…f" || result.Tool.Diff != "g…l" || result.Tool.Output != "m…r" || result.Tool.Summary != "s…y" {
		t.Fatalf("bounded event = %#v", result)
	}
	want := []string{"text", "tool.summary", "tool.input", "tool.diff", "tool.output"}
	if !reflect.DeepEqual(result.Truncated, want) {
		t.Fatalf("truncated = %#v, want %#v", result.Truncated, want)
	}
}

func TestShowBudgetStopsAtResumableEvent(t *testing.T) {
	large := strings.Repeat("x", 140_000)
	session := &model.Session{ID: "large-session", Agent: model.AgentClaude, Events: []model.Event{
		{Kind: model.EventUser, Text: large},
		{Kind: model.EventAssistantText, Text: large},
		{Kind: model.EventAssistantText, Text: large},
	}}
	node := indexSessionGraphs([]*model.Session{session})[0]
	response, err := buildShowResponse(node, []graphNode{node}, showOptions{limit: 200, maxText: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Events) != 1 || response.Page.NextOffset != 1 || !response.Page.HasMore || response.Page.Complete {
		t.Fatalf("budgeted response page = %#v, events = %d", response.Page, len(response.Events))
	}
}

func TestShowBudgetBoundsOneFullEvent(t *testing.T) {
	session := &model.Session{ID: "oversized-session", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: strings.Repeat("x", machineResponseBudgetBytes*2)}}}
	node := indexSessionGraphs([]*model.Session{session})[0]
	response, err := buildShowResponse(node, []graphNode{node}, showOptions{limit: 1, maxText: 0})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded)+1 > machineResponseBudgetBytes || len(response.Events) != 1 || !slices.Contains(response.Events[0].Truncated, "text") || response.Page.NextOffset != 1 {
		t.Fatalf("encoded bytes = %d, event = %#v, page = %#v", len(encoded)+1, response.Events, response.Page)
	}
}

func TestShowFailsSafelyWhenUnboundedMetadataExceedsBudget(t *testing.T) {
	tests := []*model.Session{
		{ID: "large-session", Agent: model.AgentClaude, Models: []string{strings.Repeat("m", machineResponseBudgetBytes*2)}},
		{ID: "large-event", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventToolCall, ToolName: strings.Repeat("t", machineResponseBudgetBytes*2)}}},
	}
	for _, session := range tests {
		t.Run(session.ID, func(t *testing.T) {
			registry := &fakeRegistry{sessions: []*model.Session{session}}
			var stdout, stderr bytes.Buffer
			err := Execute(context.Background(), []string{"show", session.ID}, &stdout, &stderr, func(context.Context, Options) (Registry, error) { return registry, nil })
			if errorCode(err) != "internal" || stdout.Len() != 0 || !strings.Contains(stderr.String(), "response budget") {
				t.Fatalf("error = %v, stdout bytes = %d, stderr = %q", err, stdout.Len(), stderr.String())
			}
		})
	}
}

func TestShowTotalsSeparateSelfAndDescendants(t *testing.T) {
	child := &model.Session{Usage: []model.Usage{{InputTokens: 20}}, Cost: model.Cost{USD: 2, Estimated: true}}
	root := &model.Session{Usage: []model.Usage{{InputTokens: 10}}, Cost: model.Cost{USD: 1}, Subagents: []*model.Session{child}}
	totals := showTotals(root)
	if totals.Tokens.Self.Total != 10 || totals.Tokens.Descendants.Total != 20 || totals.Tokens.Total.Total != 30 {
		t.Fatalf("token totals = %#v", totals.Tokens)
	}
	if totals.Cost.Self.USD != 1 || totals.Cost.Descendants.USD != 2 || totals.Cost.Total.USD != 3 || !totals.Cost.Total.Estimated {
		t.Fatalf("cost totals = %#v", totals.Cost)
	}
}

func TestShowRawPreservesExactRecordBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	raw := []byte(`{ "z": 1, "a": [2, 3] }`)
	if err := os.WriteFile(path, append(append([]byte(nil), raw...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{ID: "raw-session", Agent: model.AgentClaude, Path: path, Events: []model.Event{{
		Kind: model.EventUser, RecordRef: model.RecordRef{Path: path, Length: int64(len(raw)), Digest: sha256.Sum256(raw)},
	}}}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"show", "raw-session", "--raw", "0"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response RawResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RawRecord.RawJSON != string(raw) {
		t.Fatalf("raw_json = %q, want %q", response.RawRecord.RawJSON, raw)
	}
}

func TestShowRawReportsUnavailableAndChangedRecords(t *testing.T) {
	unavailable := &model.Session{ID: "aggregate-session", Agent: model.AgentCodex, Events: []model.Event{{Kind: model.EventUsage}}}
	var stderr bytes.Buffer
	err := Execute(context.Background(), []string{"show", "aggregate-session", "--raw", "0"}, io.Discard, &stderr, func(context.Context, Options) (Registry, error) {
		return &fakeRegistry{sessions: []*model.Session{unavailable}}, nil
	})
	if errorCode(err) != "record_unavailable" || exitCode(err) != 1 {
		t.Fatalf("unavailable error = %#v, stderr = %q", err, stderr.String())
	}

	path := filepath.Join(t.TempDir(), "changed.jsonl")
	original := []byte(`{"value":"first"}`)
	changed := []byte(`{"value":"later"}`)
	if len(original) != len(changed) {
		t.Fatal("test records differ in length")
	}
	if err := os.WriteFile(path, append(changed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	changedSession := &model.Session{ID: "changed-session", Agent: model.AgentClaude, Path: path, Events: []model.Event{{RecordRef: model.RecordRef{Path: path, Length: int64(len(original)), Digest: sha256.Sum256(original)}}}}
	err = Execute(context.Background(), []string{"show", "changed-session", "--raw", "0"}, io.Discard, io.Discard, func(context.Context, Options) (Registry, error) {
		return &fakeRegistry{sessions: []*model.Session{changedSession}}, nil
	})
	if errorCode(err) != "record_changed" {
		t.Fatalf("changed error = %#v", err)
	}
}

func TestShowSelectedUnreadableDiagnosticFails(t *testing.T) {
	path := "/logs/broken-session.jsonl"
	registry := &fakeRegistry{diagnostics: []source.DiscoveryDiagnostic{{Agent: model.AgentClaude, Path: path, Err: errors.New("invalid JSON")}}}
	var stderr bytes.Buffer
	err := Execute(context.Background(), []string{"show", path}, io.Discard, &stderr, func(context.Context, Options) (Registry, error) { return registry, nil })
	if exitCode(err) != 1 || errorCode(err) != "unreadable_session" || !strings.Contains(stderr.String(), `"code": "unreadable_session"`) {
		t.Fatalf("error = %#v, stderr = %q", err, stderr.String())
	}
}

func exitCode(err error) int {
	status, _ := ExitStatus(err)
	return status
}

func errorCode(err error) string {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Detail.Code
	}
	return ""
}
