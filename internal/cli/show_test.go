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

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
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

func TestRepeatedAgentPathChildrenKeepDistinctRefs(t *testing.T) {
	first := &model.Session{
		ID:        "thread-orbit-first",
		Agent:     model.AgentCodex,
		Title:     "First orbit review",
		AgentPath: "/root/orbit_review",
		Path:      "/fictional/orbit/first.jsonl",
	}
	second := &model.Session{
		ID:        "thread-orbit-second",
		Agent:     model.AgentCodex,
		Title:     "Second orbit review",
		AgentPath: "/root/orbit_review",
		Path:      "/fictional/orbit/second.jsonl",
	}
	root := &model.Session{
		ID:        "thread-orbit-root",
		Agent:     model.AgentCodex,
		Project:   "moon-lab",
		CWD:       "/workspace/moon-lab",
		Path:      "/fictional/orbit/root.jsonl",
		Subagents: []*model.Session{first, second},
	}
	registry := &fakeRegistry{sessions: []*model.Session{root}}

	var listOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"list", "--agent", "codex", "--all"}, &listOutput, io.Discard, func(context.Context, Options) (Registry, error) {
		return registry, nil
	}); err != nil {
		t.Fatalf("list error = %v", err)
	}
	var listResponse ListResponse
	if err := json.Unmarshal(listOutput.Bytes(), &listResponse); err != nil {
		t.Fatalf("list JSON error = %v", err)
	}
	if len(listResponse.Sessions) != 1 || listResponse.Sessions[0].Ref != "codex:thread-orbit-root" || len(listResponse.Warnings) != 0 {
		t.Fatalf("list response = %#v, want one addressable root without warnings", listResponse)
	}

	nodes := indexSessionGraphs([]*model.Session{root})
	if len(nodes) != 3 || nodes[1].ref != "codex:thread-orbit-root#orbit_review" || nodes[2].ref != "codex:thread-orbit-root#thread-orbit-second" {
		t.Fatalf("indexed refs = %#v, want agent path then thread-id fallback", nodes)
	}
	for _, want := range []struct {
		ref   string
		title string
	}{
		{ref: nodes[1].ref, title: first.Title},
		{ref: nodes[2].ref, title: second.Title},
	} {
		var output bytes.Buffer
		if err := Execute(context.Background(), []string{"show", want.ref, "--no-events"}, &output, io.Discard, func(context.Context, Options) (Registry, error) {
			return registry, nil
		}); err != nil {
			t.Fatalf("show %q error = %v", want.ref, err)
		}
		var response ShowResponse
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatalf("show %q JSON error = %v", want.ref, err)
		}
		if response.Session.Ref != want.ref || response.Session.Title != want.title {
			t.Fatalf("show %q response = %#v, want title %q", want.ref, response.Session, want.title)
		}
	}
}

func TestChildRefCollisionDoesNotHideRoot(t *testing.T) {
	children := []*model.Session{
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#first"},
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#second"},
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#third"},
	}
	root := &model.Session{ID: "thread-orbit-root", Agent: model.AgentCodex, Path: "/fictional/orbit/root.jsonl", Subagents: children}

	roots, nodes, diagnostics := addressableGraph([]*model.Session{root}, nil)
	if len(roots) != 1 || roots[0] != root || len(root.Subagents) != len(children) {
		t.Fatalf("addressable graph = roots %#v, children %#v; want root and all children", roots, root.Subagents)
	}
	if len(nodes) != 2 || nodes[0].session != root || nodes[1].session != children[0] {
		t.Fatalf("addressable nodes = %#v; want root and only the first child indexed", nodes)
	}
	if len(diagnostics) != 2 || diagnostics[0].path != children[1].Path || diagnostics[1].path != children[2].Path {
		t.Fatalf("child collision diagnostics = %#v, want the two affected child paths", diagnostics)
	}
}

func TestShowRetainsUnaddressableChildUsage(t *testing.T) {
	children := []*model.Session{
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#first", Usage: []model.Usage{{InputTokens: 2}}},
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#second", Usage: []model.Usage{{InputTokens: 3}}},
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#third", Usage: []model.Usage{{InputTokens: 4}}},
	}
	root := &model.Session{
		ID: "thread-orbit-root", Agent: model.AgentCodex, Path: "/fictional/orbit/root.jsonl", Usage: []model.Usage{{InputTokens: 1}}, Subagents: children,
	}
	registry := &fakeRegistry{sessions: []*model.Session{root}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"show", "codex:thread-orbit-root", "--no-events"}, &output, io.Discard, func(context.Context, Options) (Registry, error) {
		return registry, nil
	}); err != nil {
		t.Fatal(err)
	}
	var response ShowResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Session.Subagents != 3 || !reflect.DeepEqual(response.SubagentRefs, []string{"codex:thread-orbit-root#orbit_review"}) {
		t.Fatalf("show response = %#v; want all children counted and only the addressable ref listed", response)
	}
	if response.Totals.Tokens.Total.Total != 10 {
		t.Fatalf("show totals = %#v; want usage from all three children", response.Totals)
	}
}

func TestShowListsAndResolvesNestedWorkflowRef(t *testing.T) {
	parser := claude.NewParser(cost.NewCalculator(cost.Table{}))
	fixture := filepath.Join("..", "source", "claude", "testdata", "workflow", "subagents", "session-workflow.jsonl")
	root, err := parser.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	registry := &fakeRegistry{sessions: []*model.Session{root}, load: func(session *model.Session) error {
		return parser.LoadNodeEvents(context.Background(), session)
	}}
	wantRef := "claude:session-workflow#wf-river-run/nested-mapper"
	nodes := indexSessionGraphs([]*model.Session{root})
	for _, selector := range []string{"wf-river-run", "wf-river"} {
		selected, resolveErr := resolveSelector(selector, nodes, nil)
		if resolveErr != nil || selected.ref != "claude:session-workflow#wf-river-run" {
			t.Fatalf("resolveSelector(%q) = %#v, %v; want workflow group", selector, selected, resolveErr)
		}
	}

	var groupOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"show", "claude:session-workflow#wf-river-run"}, &groupOutput, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var groupResponse ShowResponse
	if err := json.Unmarshal(groupOutput.Bytes(), &groupResponse); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(groupResponse.SubagentRefs, []string{wantRef}) {
		t.Fatalf("subagent_refs = %#v, want nested workflow ref", groupResponse.SubagentRefs)
	}
	if groupResponse.Session.Title != "River survey" {
		t.Fatalf("group title = %q, want workflow summary title", groupResponse.Session.Title)
	}

	var summaryOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"show", "claude:session-workflow#wf-river-run", "--no-events"}, &summaryOutput, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var summaryResponse ShowResponse
	if err := json.Unmarshal(summaryOutput.Bytes(), &summaryResponse); err != nil {
		t.Fatal(err)
	}
	if summaryResponse.Session.Title != "River survey" {
		t.Fatalf("summary group title = %q, want workflow summary title", summaryResponse.Session.Title)
	}

	var nestedOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"show", wantRef}, &nestedOutput, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var nestedResponse ShowResponse
	if err := json.Unmarshal(nestedOutput.Bytes(), &nestedResponse); err != nil {
		t.Fatal(err)
	}
	if nestedResponse.Session.Ref != wantRef || nestedResponse.Session.Title != "Map the river channels" {
		t.Fatalf("resolved session = %#v", nestedResponse.Session)
	}
}

func TestWorkflowGroupRunIDSelectorReportsAmbiguity(t *testing.T) {
	roots := []*model.Session{
		{ID: "session-one", Agent: model.AgentClaude, Subagents: []*model.Session{{ID: "workflow-river-alpha", Agent: model.AgentClaude, Group: true, Path: "/logs/one.jsonl#workflow-river-alpha"}}},
		{ID: "session-two", Agent: model.AgentClaude, Subagents: []*model.Session{{ID: "workflow-river-beta", Agent: model.AgentClaude, Group: true, Path: "/logs/two.jsonl#workflow-river-beta"}}},
	}
	_, err := resolveSelector("workflow-river", indexSessionGraphs(roots), nil)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Detail.Code != "ambiguous_ref" || len(exit.Detail.Candidates) != 2 {
		t.Fatalf("ambiguous workflow selector error = %#v", err)
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

func TestAddressableRootsRejectsPartialMirror(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.jsonl")
	secondPath := filepath.Join(directory, "second.jsonl")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("shared transcript\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shared := model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared"}
	first := &model.Session{
		ID: "duplicate", Agent: model.AgentClaude, Path: firstPath, SourceSize: 18,
		Requests:  []model.RequestUsage{shared},
		Subagents: []*model.Session{{ID: "reviewer", Agent: model.AgentClaude, Title: "Shared", Path: firstPath + "#reviewer"}},
	}
	second := &model.Session{
		ID: "duplicate", Agent: model.AgentClaude, Path: secondPath, SourceSize: 18,
		Requests:  []model.RequestUsage{shared, {RequestID: "unique-without-message"}},
		Subagents: []*model.Session{{ID: "reviewer", Agent: model.AgentClaude, Title: "Unique", Path: secondPath + "#reviewer"}},
	}

	roots, diagnostics := addressableRoots([]*model.Session{first, second}, nil)
	if len(roots) != 0 || len(diagnostics) == 0 {
		t.Fatalf("partial mirror roots = %#v, diagnostics = %#v; want ambiguous duplicate rejection", roots, diagnostics)
	}
}

func TestAddressableRootsRejectsEqualSummaryWithDifferentSource(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.jsonl")
	secondPath := filepath.Join(directory, "second.jsonl")
	if err := os.WriteFile(firstPath, []byte("assistant text alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("assistant text bravo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := func(path string) *model.Session {
		return &model.Session{
			ID: "duplicate", Agent: model.AgentClaude, Path: path, SourceSize: 21,
			Title: "Same summary", Messages: 1,
			Requests: []model.RequestUsage{{MessageID: "message-shared", RequestID: "request-shared"}},
		}
	}

	roots, diagnostics := addressableRoots([]*model.Session{session(firstPath), session(secondPath)}, nil)
	if len(roots) != 0 || len(diagnostics) != 2 {
		t.Fatalf("different-source roots = %#v, diagnostics = %#v; want duplicate-ref rejection", roots, diagnostics)
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
