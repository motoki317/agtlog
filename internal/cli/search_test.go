package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

func TestSearchCoversDescendantsAndEveryToolField(t *testing.T) {
	child := &model.Session{
		ID: "child-thread", Agent: model.AgentCodex, AgentPath: "/root/review_x", Project: "forge", Title: "Review",
		Events: []model.Event{{
			Timestamp: time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC), Kind: model.EventToolCall, ToolName: "exec_command",
			Detail:        &model.ToolDetail{Input: "inspect watcher race", Diff: "fix watcher race", Output: "Watcher Race then watcher race"},
			ResultSummary: "watcher race resolved",
		}},
	}
	root := &model.Session{ID: "root-thread", Agent: model.AgentCodex, Project: "forge", Title: "Root", UpdatedAt: time.Now(), Subagents: []*model.Session{child}}
	registry := &fakeRegistry{sessions: []*model.Session{root}}
	var loads atomic.Int32
	registry.load = func(*model.Session) error { loads.Add(1); return nil }
	var output bytes.Buffer
	err := Execute(context.Background(), []string{"search", "watcher race", "--session", "root-thread", "--all", "--snippet", "0"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil })
	if err != nil {
		t.Fatal(err)
	}
	var response SearchResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("session graph loaded %d times, want once", loads.Load())
	}
	fields := make([]string, 0, len(response.Hits))
	for _, hit := range response.Hits {
		fields = append(fields, hit.Field)
		if hit.Session.Ref != "codex:root-thread#review_x" || hit.Event.Index != 0 {
			t.Fatalf("hit address = %#v", hit)
		}
	}
	wantFields := []string{"tool.input", "tool.diff", "tool.output", "tool.summary"}
	if strings.Join(fields, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("fields = %#v, want %#v", fields, wantFields)
	}
	if response.Hits[2].Matches != 2 || response.Hits[2].Snippet != "Watcher Race…" {
		t.Fatalf("output hit = %#v", response.Hits[2])
	}
	if !response.Page.Complete || response.Page.Total == nil || *response.Page.Total != 4 || response.Page.SessionsScanned != 2 || response.Page.SessionsMatched != 1 {
		t.Fatalf("page = %#v", response.Page)
	}
}

func TestSearchUsesTheSameFieldProjectionAsShow(t *testing.T) {
	const displayed = "Read(/workspace/internal/model/model.go)"
	event := model.Event{
		Kind:      model.EventToolCall,
		ToolName:  "Read",
		ToolInput: "/workspace/internal/model/model.go",
	}
	session := &model.Session{ID: "session-fields", Agent: model.AgentClaude, UpdatedAt: time.Now(), Events: []model.Event{event}}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	var searchOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"search", displayed, "--session", "session-fields"}, &searchOutput, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var searchResponse SearchResponse
	if err := json.Unmarshal(searchOutput.Bytes(), &searchResponse); err != nil {
		t.Fatal(err)
	}
	if len(searchResponse.Hits) != 1 || searchResponse.Hits[0].Field != fieldText || searchResponse.Hits[0].Range != [2]int{0, len([]rune(displayed))} || searchResponse.Hits[0].Snippet != displayed {
		t.Fatalf("hits = %#v", searchResponse.Hits)
	}

	var showOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"show", "session-fields", "--offset", "0", "--limit", "1", "--full"}, &showOutput, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var showResponse ShowResponse
	if err := json.Unmarshal(showOutput.Bytes(), &showResponse); err != nil {
		t.Fatal(err)
	}
	if len(showResponse.Events) != 1 || showResponse.Events[0].Text != displayed {
		t.Fatalf("events = %#v", showResponse.Events)
	}
	for _, field := range eventSearchFields(event) {
		var shown string
		switch field.name {
		case fieldText:
			shown = showResponse.Events[0].Text
		case fieldToolInput:
			shown = showResponse.Events[0].Tool.Input
		case fieldToolDiff:
			shown = showResponse.Events[0].Tool.Diff
		case fieldToolOutput:
			shown = showResponse.Events[0].Tool.Output
		case fieldToolSummary:
			shown = showResponse.Events[0].Tool.Summary
		}
		if field.text != shown {
			t.Fatalf("field %s search text %q differs from show text %q", field.name, field.text, shown)
		}
	}
}

func TestSearchAllStopsAtResponseBudgetAndResumesTruthfully(t *testing.T) {
	events := make([]model.Event, 1200)
	for index := range events {
		events[index] = model.Event{Kind: model.EventUser, Text: fmt.Sprintf("match-%04d %s", index, strings.Repeat("context ", 80))}
	}
	session := &model.Session{ID: "session-budget", Agent: model.AgentClaude, UpdatedAt: time.Now(), Events: events}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	run := func(offset int) ([]byte, SearchResponse) {
		args := []string{"search", "match", "--session", "session-budget", "--all"}
		if offset > 0 {
			args = append(args, "--offset", strconv.Itoa(offset))
		}
		var output bytes.Buffer
		if err := Execute(context.Background(), args, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
			t.Fatal(err)
		}
		var response SearchResponse
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return output.Bytes(), response
	}
	offset := 0
	seen := 0
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > len(events) {
			t.Fatal("pagination did not terminate")
		}
		encoded, response := run(offset)
		if len(encoded) > machineResponseBudgetBytes || response.Page.Returned == 0 {
			t.Fatalf("page %d bytes = %d, page = %#v", pageNumber, len(encoded), response.Page)
		}
		for _, hit := range response.Hits {
			if hit.Event.Index != seen {
				t.Fatalf("page %d hit index = %d, want %d", pageNumber, hit.Event.Index, seen)
			}
			seen++
		}
		if response.Page.NextOffset != offset+response.Page.Returned {
			t.Fatalf("page %d = %#v", pageNumber, response.Page)
		}
		if response.Page.Complete {
			if response.Page.HasMore || response.Page.Total == nil || *response.Page.Total != len(events) || response.Page.NextOffset != len(events) {
				t.Fatalf("final page = %#v", response.Page)
			}
			break
		}
		if !response.Page.HasMore || response.Page.Total != nil {
			t.Fatalf("bounded page %d = %#v", pageNumber, response.Page)
		}
		offset = response.Page.NextOffset
	}
	if seen != len(events) {
		t.Fatalf("saw %d hits, want %d", seen, len(events))
	}
}

func TestSearchFailsClosedWhenOneHitExceedsResponseBudget(t *testing.T) {
	text := strings.Repeat("a", machineResponseBudgetBytes) + "oversized-needle" + strings.Repeat("b", machineResponseBudgetBytes)
	session := &model.Session{ID: "session-oversized-hit", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: text}}}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	factory := func(context.Context, Options) (Registry, error) { return registry, nil }

	var control bytes.Buffer
	if err := Execute(context.Background(), []string{"search", "oversized-needle", "--session", "session-oversized-hit", "--snippet", "0"}, &control, io.Discard, factory); err != nil {
		t.Fatal(err)
	}
	if control.Len() > machineResponseBudgetBytes {
		t.Fatalf("control bytes = %d", control.Len())
	}

	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"search", "oversized-needle", "--session", "session-oversized-hit", "--all", "--snippet", strconv.Itoa(machineResponseBudgetBytes)}, &stdout, &stderr, factory)
	if exitCode(err) != 1 || errorCode(err) != "internal" || stdout.Len() != 0 || stderr.Len() > machineResponseBudgetBytes {
		t.Fatalf("error = %#v, stdout bytes = %d, stderr = %q", err, stdout.Len(), stderr.String())
	}
}

func TestSearchDefaultMatchReportsRuneRange(t *testing.T) {
	matcher, err := newTextMatcher("watcher race", false, false)
	if err != nil {
		t.Fatal(err)
	}
	start, end, count, ok := matcher.find("火 WATCHER race and watcher RACE")
	if !ok || start != 2 || end != 14 || count != 2 {
		t.Fatalf("find() = %d, %d, %d, %v", start, end, count, ok)
	}
}

func TestSearchRegexAndCaseSensitivity(t *testing.T) {
	insensitive, err := newTextMatcher(`error [0-9]+`, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, count, ok := insensitive.find("ERROR 12 and error 34"); !ok || count != 2 {
		t.Fatalf("case-insensitive regex count = %d, found = %v", count, ok)
	}
	sensitive, err := newTextMatcher(`ERROR [0-9]+`, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, count, ok := sensitive.find("ERROR 12 and error 34"); !ok || count != 1 {
		t.Fatalf("case-sensitive regex count = %d, found = %v", count, ok)
	}
	if _, err := newTextMatcher("[", true, false); errorCode(err) != "usage" {
		t.Fatalf("invalid regex error = %#v", err)
	}
}

func TestSearchUnicodeFoldAndDenseRegexUseCorrectRanges(t *testing.T) {
	folded, err := newTextMatcher("kélvin", false, false)
	if err != nil {
		t.Fatal(err)
	}
	start, end, count, ok := folded.find("x KÉLVIN and Kélvin")
	if !ok || start != 2 || end != 8 || count != 2 {
		t.Fatalf("folded match = %d:%d count %d found %t", start, end, count, ok)
	}
	zeroWidth, err := newTextMatcher("(?m)^", true, true)
	if err != nil {
		t.Fatal(err)
	}
	start, end, count, ok = zeroWidth.find("one\ntwo\nthree")
	if !ok || start != 0 || end != 0 || count != 3 {
		t.Fatalf("regex match = %d:%d count %d found %t", start, end, count, ok)
	}
}

func TestMatchSnippetHandlesMaximumContextWithoutOverflow(t *testing.T) {
	if got := matchSnippet("alpha match omega", 6, 11, int(^uint(0)>>1)); got != "alpha match omega" {
		t.Fatalf("matchSnippet() = %q", got)
	}
}

func TestSearchCommitsConcurrentResultsInCandidateOrder(t *testing.T) {
	newer := &model.Session{ID: "newer-session", Agent: model.AgentClaude, UpdatedAt: time.Unix(20, 0), Events: []model.Event{{Kind: model.EventUser, Text: "signal"}}}
	older := &model.Session{ID: "older-session", Agent: model.AgentCodex, UpdatedAt: time.Unix(10, 0), Events: []model.Event{{Kind: model.EventUser, Text: "signal"}}}
	registry := &fakeRegistry{sessions: []*model.Session{older, newer}}
	var completionMu sync.Mutex
	var completions []string
	registry.load = func(session *model.Session) error {
		if session == newer {
			time.Sleep(40 * time.Millisecond)
		}
		completionMu.Lock()
		completions = append(completions, session.ID)
		completionMu.Unlock()
		return nil
	}
	run := func() []byte {
		var output bytes.Buffer
		err := Execute(context.Background(), []string{"search", "signal", "--limit", "1"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil })
		if err != nil {
			t.Fatal(err)
		}
		return append([]byte(nil), output.Bytes()...)
	}
	first := run()
	second := run()
	if !bytes.Equal(first, second) {
		t.Fatalf("concurrent output changed:\n%s\n%s", first, second)
	}
	completionMu.Lock()
	if len(completions) < 2 || completions[0] != "older-session" {
		t.Fatalf("test did not force out-of-order completion: %#v", completions)
	}
	completionMu.Unlock()
	var response SearchResponse
	if err := json.Unmarshal(first, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Hits) != 1 || response.Hits[0].Session.Ref != "claude:newer-session" || !response.Page.HasMore || response.Page.Complete || response.Page.Total != nil {
		t.Fatalf("ordered response = %#v", response)
	}
}

func TestSearchOffsetAndCompleteTotal(t *testing.T) {
	session := &model.Session{ID: "paging-session", Agent: model.AgentClaude, UpdatedAt: time.Now(), Events: []model.Event{
		{Kind: model.EventUser, Text: "match one"},
		{Kind: model.EventAssistantText, Text: "match two"},
		{Kind: model.EventAssistantText, Text: "match three"},
	}}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"search", "match", "--offset", "1", "--limit", "5"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response SearchResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Hits) != 2 || response.Hits[0].Event.Index != 1 || !response.Page.Complete || response.Page.Total == nil || *response.Page.Total != 3 || response.Page.NextOffset != 3 {
		t.Fatalf("response = %#v", response)
	}
}

func TestSearchPagingDoesNotOverflow(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	session := &model.Session{ID: "paging-session", Agent: model.AgentClaude, Events: []model.Event{{Kind: model.EventUser, Text: "match"}}}
	registry := &fakeRegistry{sessions: []*model.Session{session}}
	var output bytes.Buffer
	args := []string{"search", "match", "--offset", strconv.Itoa(maximumInt), "--limit", strconv.Itoa(maximumInt)}
	if err := Execute(context.Background(), args, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response SearchResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Page.Returned != 0 || response.Page.NextOffset != maximumInt || !response.Page.Complete || response.Page.Total == nil || *response.Page.Total != 1 {
		t.Fatalf("page = %#v", response.Page)
	}
}

func TestSearchAcceptsDashPatternAfterDoubleDash(t *testing.T) {
	options, pattern, err := parseSearchOptions([]string{"--case-sensitive", "--", "-needle"}, io.Discard)
	if err != nil || pattern != "-needle" || !options.caseSensitive {
		t.Fatalf("parseSearchOptions() = %#v, %q, %v", options, pattern, err)
	}
}

func TestSearchBroadLoadFailureWarnsButScopedFails(t *testing.T) {
	broken := &model.Session{ID: "broken-session", Agent: model.AgentClaude, UpdatedAt: time.Now()}
	registry := &fakeRegistry{sessions: []*model.Session{broken}, load: func(*model.Session) error { return errors.New("invalid record") }}
	var broadOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"search", "needle"}, &broadOutput, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var broad SearchResponse
	if err := json.Unmarshal(broadOutput.Bytes(), &broad); err != nil {
		t.Fatal(err)
	}
	if len(broad.Warnings) != 1 || broad.Warnings[0].Ref != "claude:broken-session" || broad.Page.Complete || broad.Page.Total != nil {
		t.Fatalf("broad response = %#v", broad)
	}

	var stderr bytes.Buffer
	err := Execute(context.Background(), []string{"search", "needle", "--session", "broken-session"}, io.Discard, &stderr, func(context.Context, Options) (Registry, error) { return registry, nil })
	if exitCode(err) != 1 || errorCode(err) != "unreadable_session" || !strings.Contains(stderr.String(), `"code": "unreadable_session"`) {
		t.Fatalf("scoped error = %#v, stderr = %q", err, stderr.String())
	}
}

func TestSearchScopedFailsForKnownUnreadableDescendant(t *testing.T) {
	root := &model.Session{
		ID:        "root-session",
		Agent:     model.AgentClaude,
		Path:      "/logs/project-orbit/root-session.jsonl",
		UpdatedAt: time.Now(),
	}
	registry := &fakeRegistry{
		sessions: []*model.Session{root},
		diagnostics: []source.DiscoveryDiagnostic{{
			Agent: model.AgentClaude,
			Path:  "/logs/project-orbit/root-session/subagents/agent-broken.jsonl",
			Err:   errors.New("session path is not a regular file"),
		}},
	}
	var stdout, stderr bytes.Buffer
	err := Execute(context.Background(), []string{"search", "needle", "--session", "root-session"}, &stdout, &stderr, func(context.Context, Options) (Registry, error) { return registry, nil })
	if exitCode(err) != 1 || errorCode(err) != "unreadable_session" || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code": "unreadable_session"`) {
		t.Fatalf("scoped error = %#v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
}

func TestSearchScopedChildIgnoresUnreadableSibling(t *testing.T) {
	child := &model.Session{
		ID:        "good-child",
		Agent:     model.AgentClaude,
		Path:      "/logs/project-orbit/root-session/subagents/agent-good.jsonl",
		UpdatedAt: time.Now(),
		Events:    []model.Event{{Kind: model.EventUser, Text: "needle"}},
	}
	root := &model.Session{
		ID:        "root-session",
		Agent:     model.AgentClaude,
		Path:      "/logs/project-orbit/root-session.jsonl",
		UpdatedAt: time.Now(),
		Subagents: []*model.Session{child},
	}
	registry := &fakeRegistry{
		sessions: []*model.Session{root},
		diagnostics: []source.DiscoveryDiagnostic{{
			Agent: model.AgentClaude,
			Path:  "/logs/project-orbit/root-session/subagents/agent-broken.jsonl",
			Err:   errors.New("session path is not a regular file"),
		}},
	}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"search", "needle", "--session", "good-child"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response SearchResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Hits) != 1 || response.Hits[0].Session.Ref != "claude:root-session#good-child" || !response.Page.Complete {
		t.Fatalf("response = %#v", response)
	}
}

func TestSearchKeepsReadableRootHitsWhenDescendantFails(t *testing.T) {
	child := &model.Session{ID: "child-session", Agent: model.AgentClaude, Path: "/logs/child.jsonl"}
	root := &model.Session{
		ID: "root-session", Agent: model.AgentClaude, Path: "/logs/root.jsonl", UpdatedAt: time.Now(), Subagents: []*model.Session{child},
		Events: []model.Event{{Kind: model.EventUser, Text: "relay signal"}},
	}
	registry := &fakeRegistry{sessions: []*model.Session{root}, load: func(session *model.Session) error {
		if session == child {
			return errors.New("fictional child failure")
		}
		return nil
	}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"search", "relay", "--all"}, &output, io.Discard, func(context.Context, Options) (Registry, error) { return registry, nil }); err != nil {
		t.Fatal(err)
	}
	var response SearchResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Hits) != 1 || response.Hits[0].Session.Ref != "claude:root-session" || len(response.Warnings) != 1 || response.Warnings[0].Ref != "claude:root-session#child-session" || response.Page.Complete || response.Page.Total != nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestSearchSkipsUnaddressableChildWithoutEmptyRefs(t *testing.T) {
	children := []*model.Session{
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#first", Events: []model.Event{{Kind: model.EventUser, Text: "collision marker"}}},
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#second", Events: []model.Event{{Kind: model.EventUser, Text: "collision marker"}}},
		{ID: "thread-duplicate", Agent: model.AgentCodex, AgentPath: "/root/orbit_review", Path: "/fictional/orbit/root.jsonl#third", Events: []model.Event{{Kind: model.EventUser, Text: "collision marker"}}},
	}
	root := &model.Session{
		ID: "thread-orbit-root", Agent: model.AgentCodex, Path: "/fictional/orbit/root.jsonl", Events: []model.Event{{Kind: model.EventUser, Text: "collision marker"}}, Subagents: children,
	}
	registry := &fakeRegistry{sessions: []*model.Session{root}}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"search", "collision marker", "--all"}, &output, io.Discard, func(context.Context, Options) (Registry, error) {
		return registry, nil
	}); err != nil {
		t.Fatal(err)
	}
	var response SearchResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Hits) != 2 || response.Hits[0].Session.Ref != "codex:thread-orbit-root" || response.Hits[1].Session.Ref != "codex:thread-orbit-root#orbit_review" {
		t.Fatalf("search hits = %#v; want root and the one addressable child", response.Hits)
	}
	for _, hit := range response.Hits {
		if hit.Session.Ref == "" {
			t.Fatalf("search returned an empty ref: %#v", hit)
		}
	}
	if len(response.Warnings) != 2 || response.Page.Complete || response.Page.Total != nil {
		t.Fatalf("search page = %#v, warnings = %#v; want two diagnostics and an incomplete page", response.Page, response.Warnings)
	}
}
