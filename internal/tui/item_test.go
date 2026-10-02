package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
	"github.com/motoki317/agtlog/internal/source/claude"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

func TestOpenItemFollowsRootLiveUpdate(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	running := []byte(`{"status":"running","padding":"` + strings.Repeat("x", 300) + `"}`)
	recordRef := writeRawRecord(t, running)
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: recordRef.Path, Project: "starship", Title: "Plan route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Timestamp: now.Add(-5 * time.Minute), Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route", RecordRef: recordRef, Detail: &model.ToolDetail{Input: "check-route", Output: "running"}},
		},
	}
	m := newModelWithClock([]*model.Session{root}, nil, func() time.Time { return now })
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 80, Height: 10}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}} {
		updated, cmd := m.Update(msg)
		m = updated.(Model)
		if cmd != nil {
			updated, _ = m.Update(cmd())
			m = updated.(Model)
		}
	}
	before := m.detail.(*itemView)
	oldOffset := before.viewport.YOffset
	oldGeneration := before.generation
	if before.wrap || oldOffset == 0 {
		t.Fatalf("pre-update item state wrap=%t offset=%d", before.wrap, oldOffset)
	}

	finished := []byte(`{"status":"finished","padding":"` + strings.Repeat("y", 300) + `"}`)
	if err := os.WriteFile(recordRef.Path, append(finished, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	finishedRef := model.RecordRef{Path: recordRef.Path, Length: int64(len(finished)), Digest: sha256.Sum256(finished)}
	replacement := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: recordRef.Path, Project: "voyage", Title: "Plan updated route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Timestamp: now.Add(-7 * time.Minute), Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route", RecordRef: finishedRef, Detail: &model.ToolDetail{Input: "check-route", Output: "finished"}},
		},
	}
	updated, cmd := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("live refresh did not re-request the visible raw record")
	}
	firstRawCmd := cmd
	updated, cmd = m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("second live refresh did not re-request the visible raw record")
	}
	updated, _ = m.Update(firstRawCmd())
	m = updated.(Model)
	if item := m.detail.(*itemView); !item.rawLoading || len(item.raw) != 0 {
		t.Fatalf("refreshed item accepted generation %d into generation %d", oldGeneration+1, item.generation)
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	view := ansi.Strip(m.View())
	item, ok := m.detail.(*itemView)
	if !ok || len(m.detailStack) != 1 {
		t.Fatalf("live update left screen=%T stack=%d, want refreshed item", m.detail, len(m.detailStack))
	}
	if item.wrap || item.viewport.YOffset != oldOffset {
		t.Fatalf("refreshed item state wrap=%t offset=%d, want wrap=false offset=%d", item.wrap, item.viewport.YOffset, oldOffset)
	}
	for _, want := range []string{"voyage › Plan updated route › Bash", "Raw"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refreshed item missing %q:\n%s", want, view)
		}
	}
	var refreshedLines []string
	for _, line := range item.lines {
		refreshedLines = append(refreshedLines, line.text)
	}
	content := strings.Join(refreshedLines, "\n")
	for _, want := range []string{"finished", "relative time  7m", `"status": "finished"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("refreshed item content missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "running") {
		t.Fatalf("refreshed item retained stale output:\n%s", content)
	}
}

func TestItemNavigationDuringRawRefreshKeepsLatestOffset(t *testing.T) {
	item := newItemView(model.Event{
		Kind:      model.EventSystem,
		Text:      strings.Repeat("route status\n", 24),
		RecordRef: model.RecordRef{Path: "/fictional/session.jsonl", Length: 18},
	}, model.AgentCodex, nil, 80, 8, newStyles())
	if cmd := item.requestRaw(); cmd == nil {
		t.Fatal("raw request did not enter the loading state")
	}
	item.viewport.SetYOffset(5)
	restoreOffset := 9
	item.restoreYOffset = &restoreOffset

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	item.update(rawRecordLoadedMsg{record: []byte(`{"status":"ready"}`)})

	if item.viewport.YOffset != 0 {
		t.Fatalf("raw completion restored offset %d after navigation, want latest offset 0", item.viewport.YOffset)
	}
}

func TestOpenItemFallsBackWhenEventDisappears(t *testing.T) {
	root := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Check the route"},
			{Kind: model.EventToolCall, CallID: "call-route", ToolName: "Bash", ToolInput: "check-route"},
		},
	}
	m := NewModel([]*model.Session{root}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	replacement := &model.Session{
		ID: "route", Agent: model.AgentClaude, Path: "/workspace/route.jsonl",
		Events: []model.Event{{Kind: model.EventUser, Text: "Check the route"}},
	}
	updated, _ := m.Update(source.SessionUpdate{Sessions: []*model.Session{replacement}})
	m = updated.(Model)

	if detailStateFromScreen(t, m.detail).session != replacement || len(m.detailStack) != 0 {
		t.Fatalf("removed item event left screen=%T stack=%d, want refreshed root detail", m.detail, len(m.detailStack))
	}
}

func TestItemMouseWheelScrollsAndClicksDoNothing(t *testing.T) {
	item := newItemView(model.Event{Kind: model.EventThinking, Text: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine"}, model.AgentCodex, nil, 80, 8, newStyles())
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	if got := m.detail.(*itemView).viewport.YOffset; got != mouseWheelRows {
		t.Fatalf("item wheel down offset = %d, want %d", got, mouseWheelRows)
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: 2, Y: 2})
	m = updated.(Model)
	if _, ok := m.detail.(*itemView); !ok || m.detail.(*itemView).viewport.YOffset != mouseWheelRows {
		t.Fatalf("item click detail=%T offset=%d, want unchanged item", m.detail, m.detail.(*itemView).viewport.YOffset)
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = updated.(Model)
	if got := m.detail.(*itemView).viewport.YOffset; got != 0 {
		t.Fatalf("item wheel up offset = %d, want 0", got)
	}
}

func TestItemShiftWheelStillScrollsVertically(t *testing.T) {
	item := newItemView(model.Event{Kind: model.EventThinking, Text: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine"}, model.AgentCodex, nil, 80, 8, newStyles())
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, Shift: true})
	m = updated.(Model)
	if got := m.detail.(*itemView).viewport.YOffset; got != mouseWheelRows {
		t.Fatalf("shift+wheel item offset = %d, want %d", got, mouseWheelRows)
	}
}

func TestEnterOnToolOpensFullItemView(t *testing.T) {
	input := make([]string, detailPreviewLineCap+2)
	diff := make([]string, detailPreviewLineCap+2)
	output := make([]string, detailPreviewLineCap+2)
	for index := range input {
		input[index] = fmt.Sprintf("command-%02d", index)
		diff[index] = fmt.Sprintf("+route-%02d", index)
		output[index] = fmt.Sprintf("result-%02d", index)
	}
	session := &model.Session{
		ID: "lunar", Agent: model.AgentCodex, Project: "starship", Title: "Plan route",
		Events: []model.Event{
			{Kind: model.EventUser, Text: "Update the route"},
			{Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check route", Detail: &model.ToolDetail{
				Input: strings.Join(input, "\n"), Diff: strings.Join(diff, "\n"), Output: strings.Join(output, "\n"),
			}},
		},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 140},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	view := ansi.Strip(m.View())
	if len(m.detailStack) != 1 {
		t.Fatalf("tool open stack depth = %d, want 1:\n%s", len(m.detailStack), view)
	}
	for _, want := range []string{"starship › Plan route › Bash", "Input", "command-41", "+route-41", "Output", "result-41"} {
		if !strings.Contains(view, want) {
			t.Fatalf("tool item view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "lines hidden") {
		t.Fatalf("tool item view applied the timeline line cap:\n%s", view)
	}
}

func TestItemToolLinesUseFallbacksAndDiffRoles(t *testing.T) {
	for _, test := range []struct {
		name   string
		detail *model.ToolDetail
		roles  map[string]detailRole
	}{
		{name: "nil detail"},
		{name: "partial detail", detail: &model.ToolDetail{Diff: "-old route\n context route\n+new route"}, roles: map[string]detailRole{
			"-old route": detailDiffRemove, " context route": detailDiffContext, "+new route": detailDiffAdd,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := itemEventLines(model.Event{
				Kind: model.EventToolCall, ToolName: "Bash", ToolInput: "fallback input", ResultSummary: "fallback output", Detail: test.detail,
			}, model.AgentClaude)
			texts := make([]string, len(lines))
			seenRoles := make(map[string]bool, len(test.roles))
			for index, line := range lines {
				texts[index] = line.text
				if want, ok := test.roles[line.text]; ok {
					seenRoles[line.text] = true
					if line.role != want {
						t.Errorf("diff line %q role = %v, want %v", line.text, line.role, want)
					}
				}
			}
			for _, want := range []string{"Input", "fallback input", "Output", "fallback output"} {
				if !slices.Contains(texts, want) {
					t.Errorf("item tool lines missing fallback %q: %#v", want, texts)
				}
			}
			for text := range test.roles {
				if !seenRoles[text] {
					t.Errorf("item tool lines missing diff role for %q: %#v", text, texts)
				}
			}
		})
	}
}

func TestItemToolLinesShowInputAndDiffBeforeOutput(t *testing.T) {
	lines := itemEventLines(model.Event{
		Kind: model.EventToolCall, ToolName: "exec", ToolInput: "make build", Detail: &model.ToolDetail{
			Input: "make build\nmake test", Diff: "+build target", Output: "build ready",
		},
	}, model.AgentCodex)
	indexes := map[string]int{"Input": -1, "+build target": -1, "Output": -1}
	for index, line := range lines {
		if _, ok := indexes[line.text]; ok {
			indexes[line.text] = index
		}
	}
	if indexes["Input"] < 0 || indexes["+build target"] < 0 || indexes["Output"] < 0 ||
		indexes["Input"] >= indexes["Output"] || indexes["+build target"] >= indexes["Output"] {
		t.Fatalf("section indexes = %#v, want input and diff before output", indexes)
	}
}

func TestItemViewStartsWithPresentMetadataAndOmitsEmptyFields(t *testing.T) {
	event := model.Event{
		Kind: model.EventToolCall, Model: "gpt-5.6-sol", ToolName: "exec_command",
		CallID: "call-route", AgentID: "agent-scout", Duration: 1250 * time.Millisecond,
		ToolInput: "check route",
	}
	item := newItemView(event, model.AgentCodex, nil, 80, 18, newStyles())

	var lines []string
	for _, line := range item.lines {
		lines = append(lines, line.text)
	}
	for _, want := range []string{
		"Event",
		"kind      tool-call",
		"model     gpt-5.6-sol",
		"tool      exec_command",
		"call-id   call-route",
		"agent-id  agent-scout",
		"duration  1.2s",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("item metadata missing %q: %#v", want, lines)
		}
	}
	for _, omitted := range []string{"relative time", "absolute time"} {
		for _, line := range lines {
			if strings.HasPrefix(line, omitted) {
				t.Errorf("zero timestamp rendered %q", line)
			}
		}
	}
}

func TestItemViewUsesOrderedDetailSections(t *testing.T) {
	item := newItemView(model.Event{
		Kind: model.EventToolCall, ToolName: "exec_command",
		Detail: &model.ToolDetail{Input: "check route", Diff: "+route ready", Output: "checks passed"},
	}, model.AgentCodex, nil, 80, 18, newStyles())

	var headers []string
	for _, line := range item.lines {
		if line.role == detailHeader {
			headers = append(headers, line.text)
		}
	}
	want := []string{"Event", "Input", "Diff", "Output"}
	if !slices.Equal(headers, want) {
		t.Fatalf("item section headers = %#v, want %#v", headers, want)
	}
}

func TestItemViewSeparatesToolContentFromPrecedingSections(t *testing.T) {
	usage := model.Usage{Model: "model-a", InputTokens: 10}
	for _, test := range []struct {
		name  string
		usage *model.Usage
	}{
		{name: "event only"},
		{name: "request", usage: &usage},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := newItemView(model.Event{
				Kind: model.EventToolCall, ToolName: "exec_command", Usage: test.usage,
				ToolInput: "check route",
			}, model.AgentCodex, nil, 80, 18, newStyles())

			for index, line := range item.lines {
				if line.role != detailHeader || line.text != "Input" {
					continue
				}
				if index == 0 || item.lines[index-1].text != "" {
					t.Fatalf("Input section is not separated:\n%s", itemLinesText(item.lines))
				}
				return
			}
			t.Fatalf("Input section missing:\n%s", itemLinesText(item.lines))
		})
	}
}

func TestItemRequestSectionShowsAuditableRateArithmetic(t *testing.T) {
	usage := model.Usage{
		Model: "model-a", InputTokens: 1_200, OutputTokens: 20,
		CacheReadTokens: 1_000, InputIncludesCacheRead: true,
	}
	item := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "model-a", Usage: &usage, Priced: true,
		Cost: model.CostBreakdown{
			Input:     model.CostBuckets{{RatePerToken: 0.000005, Tokens: 200}},
			CacheRead: model.CostBuckets{{RatePerToken: 0.0000005, Tokens: 1_000}},
			Output:    model.CostBuckets{{RatePerToken: 0.000030, Tokens: 20}},
		},
	}, model.AgentCodex, nil, 80, 18, newStyles())

	text := ""
	for _, line := range item.lines {
		text += line.text + "\n"
	}
	for _, want := range []string{
		"Request",
		"tokens  ↑1000/0/200 ↓20 · ctx 1220",
		"input         200 × $5/Mtok   = $0.001",
		"cache read   1000 × $0.5/Mtok = $0.0005",
		"output         20 × $30/Mtok  = $0.0006",
		"total                         = $0.0021",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Request section missing %q:\n%s", want, text)
		}
	}
	equalsColumn := -1
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, " = $") {
			continue
		}
		if column := ansi.StringWidth(line[:strings.Index(line, "=")]); equalsColumn < 0 {
			equalsColumn = column
		} else if column != equalsColumn {
			t.Errorf("Request arithmetic equals columns = %d and %d:\n%s", equalsColumn, column, text)
		}
	}

	outputOnly := model.Event{Usage: &model.Usage{OutputTokens: 20}}
	if text := itemLinesText(itemRequestLines(outputOnly, model.AgentCodex)); !strings.Contains(text, "ctx 20") {
		t.Fatalf("output-only Request section omitted post-output context:\n%s", text)
	}
}

func TestItemRequestSectionShowsUnavailablePriceAndRequiresUsage(t *testing.T) {
	usage := model.Usage{Model: "future-model", InputTokens: 25}
	withUsage := newItemView(model.Event{
		Kind: model.EventUsage, Model: "future-model", Usage: &usage,
	}, model.AgentCodex, nil, 80, 14, newStyles())
	withoutUsage := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "future-model",
	}, model.AgentCodex, nil, 80, 14, newStyles())

	withText := itemLinesText(withUsage.lines)
	if !strings.Contains(withText, "Request") || !strings.Contains(withText, "price unavailable for future-model") {
		t.Fatalf("unpriced request did not name unavailable model:\n%s", withText)
	}
	if strings.Contains(itemLinesText(withoutUsage.lines), "Request") {
		t.Fatalf("event without usage rendered a Request section:\n%s", itemLinesText(withoutUsage.lines))
	}
}

func TestItemRequestNamesSubstitutionOnlyWhenApplied(t *testing.T) {
	usage := model.Usage{Model: "agents-a1", InputTokens: 10}
	breakdown := model.CostBreakdown{Input: model.CostBuckets{{RatePerToken: 0.000005, Tokens: 10}}}
	substituted := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "agents-a1", Usage: &usage,
		Cost: breakdown, Priced: true, CostEstimated: true, PricingModel: "gpt-5",
	}, model.AgentCodex, nil, 80, 14, newStyles())
	exactUsage := usage
	exactUsage.Model = "gpt-5"
	exact := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "gpt-5", Usage: &exactUsage,
		Cost: breakdown, Priced: true,
	}, model.AgentCodex, nil, 80, 14, newStyles())

	substitutedText := itemLinesText(substituted.lines)
	if !strings.Contains(substitutedText, "rate  priced as gpt-5 — no published rate for agents-a1") ||
		!strings.Contains(substitutedText, "~$0.00005") {
		t.Fatalf("substituted request did not disclose its rate:\n%s", substitutedText)
	}
	exactText := itemLinesText(exact.lines)
	if strings.Contains(exactText, "priced as") || strings.Contains(exactText, "published rate") || strings.Contains(exactText, "~$") {
		t.Fatalf("exact request contained estimate language:\n%s", exactText)
	}
}

func TestItemRequestWrapsWithinFortyColumns(t *testing.T) {
	usage := model.Usage{
		Model: "agents-a1", InputTokens: 450_000, OutputTokens: 20_000,
		CacheReadTokens: 300_000, InputIncludesCacheRead: true,
	}
	item := newItemView(model.Event{
		Kind: model.EventAssistantText, Model: "agents-a1", Usage: &usage,
		Cost: model.CostBreakdown{
			Input: model.CostBuckets{
				{RatePerToken: 0.000005, Tokens: 100_000},
				{RatePerToken: 0.000010, Tokens: 50_000, AboveThreshold: true},
			},
			CacheRead: model.CostBuckets{{RatePerToken: 0.0000005, Tokens: 300_000}},
			Output:    model.CostBuckets{{RatePerToken: 0.000030, Tokens: 20_000}},
		},
		Priced: true, CostEstimated: true, PricingModel: "gpt-5",
	}, model.AgentCodex, nil, 40, 18, newStyles())

	view := ansi.Strip(item.view())
	for number, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("40-column item line %d width = %d: %q", number+1, width, line)
		}
	}
	var wrappedBody strings.Builder
	for _, line := range strings.Split(view, "\n") {
		if strings.HasPrefix(line, "│") && strings.HasSuffix(line, "│") {
			wrappedBody.WriteString(strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│"), " "))
		}
	}
	if !strings.Contains(wrappedBody.String(), "rate  priced as gpt-5 — no published rate for agents-a1") {
		t.Fatalf("40-column wrapped view lost rate substitution:\n%s", view)
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	unwrapped := ansi.Strip(item.view())
	for number, line := range strings.Split(unwrapped, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("40-column unwrapped item line %d width = %d: %q", number+1, width, line)
		}
	}
	if !strings.Contains(unwrapped, "rate  priced as gpt-5") {
		t.Fatalf("40-column unwrapped view lost visible substitution prefix:\n%s", unwrapped)
	}
	if !strings.Contains(itemLinesText(item.lines), "rate  priced as gpt-5 — no published rate for agents-a1") {
		t.Fatalf("40-column source lines lost rate substitution:\n%s", itemLinesText(item.lines))
	}
}

func itemLinesText(lines []detailLine) string {
	text := make([]string, len(lines))
	for index, line := range lines {
		text[index] = line.text
	}
	return strings.Join(text, "\n")
}

func TestOpeningItemLoadsTerminalSafePrettyRawRecord(t *testing.T) {
	raw := []byte(`{"type":"system","message":{"content":"first\nsecond\u001b[2J` + "\u202e" + `unsafe"},"ready":true}`)
	session := &model.Session{
		ID: "route", Agent: model.AgentCodex,
		Events: []model.Event{{
			Kind: model.EventSystem, Text: "Route ready", RecordRef: writeRawRecord(t, raw),
		}},
	}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("opening an item with a record did not request raw")
	}
	if content := itemLinesText(m.detail.(*itemView).lines); !strings.HasSuffix(content, "Raw\nloading raw…") {
		t.Fatalf("opening item content did not end with the loading Raw section:\n%s", content)
	}

	updated, _ = m.Update(cmd())
	item := updated.(Model).detail.(*itemView)
	want := "{\n" +
		`  "type": "system",` + "\n" +
		`  "message": {` + "\n" +
		`    "content": "first\\nsecond\\u001b[2J\u202eunsafe"` + "\n" +
		"  },\n" +
		`  "ready": true` + "\n" +
		"}"
	if content := itemLinesText(item.lines); !strings.HasSuffix(content, "Raw\n"+want) {
		t.Fatalf("loaded item content did not end with terminal-safe pretty raw:\n%s", content)
	}
	if strings.Contains(itemLinesText(item.lines), "\u202e") {
		t.Fatal("loaded item content retained a terminal-unsafe format rune")
	}
}

func TestItemViewShowsBothTimesAndLoadsRawRecordAsynchronously(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"type":"user","message":{"content":"Inspect the route"}}`)
	event := model.Event{
		Timestamp: now.Add(-5 * time.Minute), Kind: model.EventUser, Text: "Inspect the route",
		RecordRef: writeRawRecord(t, raw),
	}
	item := newItemView(event, model.AgentClaude, nil, 80, 18, newStyles())
	item.setNow(now)

	plain := ansi.Strip(item.view())
	for _, want := range []string{"relative time  5m", "absolute time  Jan 2 11:55:00"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("item view missing %q before raw load:\n%s", want, plain)
		}
	}

	cmd := item.requestRaw()
	if cmd == nil || !strings.Contains(ansi.Strip(item.view()), "loading raw…") {
		t.Fatalf("raw request did not start an asynchronous load:\n%s", ansi.Strip(item.view()))
	}
	item.update(cmd())
	plain = ansi.Strip(item.view())
	for _, want := range []string{"Raw", `"type": "user"`, `"message": {`, `"content": "Inspect the route"`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("item view missing %q after raw load:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "R raw") || strings.Contains(plain, "R hide raw") {
		t.Fatalf("item view retained a raw toggle hint:\n%s", plain)
	}
}

func TestItemWithoutReadableRecordOmitsRawSection(t *testing.T) {
	for name, ref := range map[string]model.RecordRef{
		"missing reference": {},
		"unmatched offset":  {Path: "/fictional/session.jsonl", Offset: 42},
	} {
		t.Run(name, func(t *testing.T) {
			item := newItemView(model.Event{
				Kind: model.EventUsage, Text: "unattributed usage", RecordRef: ref,
			}, model.AgentCodex, nil, 80, 12, newStyles())

			if content := itemLinesText(item.lines); strings.Contains(content, "\nRaw\n") {
				t.Fatalf("usage item rendered unavailable Raw section:\n%s", content)
			}
		})
	}
}

func TestItemRawRequestReusesInFlightRead(t *testing.T) {
	raw := []byte(`{"status":"ready"}`)
	item := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, raw),
	}, model.AgentCodex, nil, 80, 12, newStyles())

	cmd := item.requestRaw()
	if cmd == nil || !item.rawLoading {
		t.Fatal("first raw request did not schedule one visible load")
	}
	if duplicate := item.requestRaw(); duplicate != nil {
		t.Fatal("second raw request duplicated the in-flight load")
	}
	item.update(cmd())
	if item.rawLoading || !bytes.Equal(item.raw, raw) {
		t.Fatal("raw completion did not cache the complete record")
	}
	if duplicate := item.requestRaw(); duplicate != nil {
		t.Fatal("loaded raw request scheduled another read")
	}
	if !strings.Contains(itemLinesText(item.lines), `"status": "ready"`) {
		t.Fatal("cached raw record was not visible after loading")
	}
}

func TestItemViewIgnoresRawKey(t *testing.T) {
	event := model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, []byte(`{"status":"ready"}`)),
	}
	m := NewModel([]*model.Session{{ID: "route", Agent: model.AgentCodex, Events: []model.Event{event}}}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	before := m.View()

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("R returned a command in the item view")
	}
	if after := m.View(); after != before {
		t.Fatalf("R changed the item view:\nbefore:\n%s\nafter:\n%s", ansi.Strip(before), ansi.Strip(after))
	}
}

func TestItemConstructorDoesNotClaimUnscheduledRawLoad(t *testing.T) {
	event := model.Event{
		Kind:      model.EventSystem,
		RecordRef: model.RecordRef{Path: "/fictional/session.jsonl", Length: 24},
	}
	item := newItemViewWithState(event, model.AgentCodex, nil, 80, 12, newStyles(), time.Time{}, true)

	if item.rawLoading {
		t.Fatal("state-preserving constructor marked raw loading without returning a command")
	}
	if content := itemLinesText(item.lines); !strings.HasSuffix(content, "\nRaw\n") {
		t.Fatalf("valid record reference did not render an unconditional Raw section:\n%s", content)
	}
	if cmd := item.requestRaw(); cmd == nil || !item.rawLoading {
		t.Fatal("requestRaw did not pair the loading state with a command")
	}
	if cmd := item.requestRaw(); cmd != nil {
		t.Fatal("requestRaw scheduled a duplicate in-flight load")
	}
}

func TestItemViewReportsRawReadFailuresWithoutFallback(t *testing.T) {
	tests := []struct {
		name    string
		replace func(t *testing.T, path string)
	}{
		{
			name: "missing file",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "non-regular file",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`{"status":"authoritative"}`)
			ref := writeRawRecord(t, raw)
			test.replace(t, ref.Path)
			item := newItemView(model.Event{
				Kind: model.EventSystem, Text: "derived fallback", RecordRef: ref,
			}, model.AgentCodex, nil, 80, 12, newStyles())

			cmd := item.requestRaw()
			if cmd == nil {
				t.Fatal("raw request did not schedule the failing read")
			}
			item.update(cmd())
			texts := make([]string, 0, len(item.lines))
			for _, line := range item.lines {
				texts = append(texts, line.text)
			}
			content := strings.Join(texts, "\n")
			rawIndex := slices.Index(texts, "Raw")
			if rawIndex < 0 || rawIndex+1 >= len(texts) || texts[rawIndex+1] != "raw unavailable: read failed" {
				t.Fatalf("item content did not name the read failure:\n%s", content)
			}
			if strings.Contains(content, string(raw)) {
				t.Fatalf("item content presented fallback raw after a read failure:\n%s", content)
			}
		})
	}
}

func TestItemViewReportsChangedRawWithoutFallback(t *testing.T) {
	original := []byte(`{"message":"original"}`)
	ref := writeRawRecord(t, original)
	changed := []byte(`{"message":"modified"}`)
	if err := os.WriteFile(ref.Path, append(changed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	item := newItemView(model.Event{Kind: model.EventSystem, RecordRef: ref}, model.AgentClaude, nil, 80, 12, newStyles())

	cmd := item.requestRaw()
	item.update(cmd())

	content := make([]string, 0, len(item.lines))
	for _, line := range item.lines {
		content = append(content, line.text)
	}
	rendered := strings.Join(content, "\n")
	if !strings.Contains(rendered, "raw unavailable: source changed") || strings.Contains(rendered, "original") || strings.Contains(rendered, "modified") {
		t.Fatalf("changed raw rendered a fallback body:\n%s", rendered)
	}
}

func TestCurrentItemDiscardsPreviousItemsRawResult(t *testing.T) {
	rawA := []byte(`{"item":"a"}`)
	itemA := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, rawA),
	}, model.AgentCodex, nil, 80, 12, newStyles())
	itemA.generation = 1
	cmdA := itemA.requestRaw()

	rawB := []byte(`{"item":"b"}`)
	itemB := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, rawB),
	}, model.AgentCodex, nil, 80, 12, newStyles())
	itemB.generation = 2
	cmdB := itemB.requestRaw()
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = itemB

	updated, _ := m.Update(cmdA())
	m = updated.(Model)
	current := m.detail.(*itemView)
	if !current.rawLoading || len(current.raw) != 0 {
		t.Fatalf("current item accepted generation 1 while generation %d was loading", current.generation)
	}

	updated, _ = m.Update(cmdB())
	current = updated.(Model).detail.(*itemView)
	if current.rawLoading || !bytes.Equal(current.raw, rawB) {
		t.Fatalf("current item raw = %q loading=%t, want item B", current.raw, current.rawLoading)
	}
}

func TestFullFidelityCompactionSummaryAcrossParserTimelineAndItem(t *testing.T) {
	summary := "first-" + strings.Repeat("界", 5_000) + "-last"
	line := `{"type":"user","timestamp":"2026-01-02T03:04:05Z","isCompactSummary":true,"message":{"content":` + strconv.Quote(summary) + `}}`
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &model.Session{Path: path, Agent: model.AgentClaude}
	parser := claude.NewParser(cost.NewCalculator(cost.Table{}))

	if err := parser.LoadEvents(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(session.Events) != 1 || session.Events[0].Text != summary {
		t.Fatalf("parsed compaction summary has %d runes, want %d", len([]rune(session.Events[0].Text)), len([]rune(summary)))
	}
	bounded := string([]rune(summary)[:detailPreviewRuneCap-1]) + "…"
	detail := newDetailState(session, 80, 12, newStyles())
	foundBounded := false
	for _, timelineLine := range detail.lines {
		foundBounded = foundBounded || strings.Contains(timelineLine.text, bounded)
		if strings.Contains(timelineLine.text, summary) {
			t.Fatal("timeline rendered the unbounded compaction summary")
		}
	}
	if !foundBounded {
		t.Fatal("timeline did not render the 4096-rune trailing-ellipsis form")
	}

	item := newItemView(session.Events[0], model.AgentClaude, nil, 80, 12, newStyles())
	foundFull := false
	for _, itemLine := range item.lines {
		foundFull = foundFull || itemLine.text == summary
	}
	if !foundFull {
		t.Fatal("item view did not retain the complete compaction summary")
	}
	cmd := item.requestRaw()
	item.update(cmd())
	if string(item.raw) != line {
		t.Fatalf("raw item bytes differ from fixture line: got %d bytes, want %d", len(item.raw), len(line))
	}

	changed := []byte(line)
	changed[len(changed)-2] ^= 1
	if err := os.WriteFile(path, append(changed, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	changedItem := newItemView(session.Events[0], model.AgentClaude, nil, 80, 12, newStyles())
	cmd = changedItem.requestRaw()
	changedItem.update(cmd())
	content := make([]string, 0, len(changedItem.lines))
	for _, itemLine := range changedItem.lines {
		content = append(content, itemLine.text)
	}
	if got := strings.Join(content, "\n"); !strings.Contains(got, "raw unavailable: source changed") || strings.Contains(got, line) {
		t.Fatalf("rewritten fixture rendered stale raw content:\n%s", got)
	}
}

func TestItemViewDiscardsRawResultAfterBackNavigation(t *testing.T) {
	raw := []byte(`{"message":"source"}`)
	event := model.Event{Kind: model.EventSystem, Text: "Derived", RecordRef: writeRawRecord(t, raw)}
	session := &model.Session{ID: "route", Agent: model.AgentClaude, Events: []model.Event{event}}
	m := NewModel([]*model.Session{session}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("opening the item did not return a load command")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	if _, itemOpen := m.detail.(*itemView); itemOpen {
		t.Fatal("late raw result reopened or mutated the item after back navigation")
	}
}

func TestItemRawPanelKeepsEncryptedTokensSourceExact(t *testing.T) {
	token := "gAAAA" + strings.Repeat("A", 70)
	raw := []byte(`{"secret":` + strconv.Quote(token) + `}`)
	item := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, raw),
	}, model.AgentCodex, nil, 80, 12, newStyles())
	cmd := item.requestRaw()
	item.update(cmd())

	plain := ansi.Strip(item.view())
	if string(item.raw) != string(raw) || strings.Contains(plain, "<encrypted 75 chars>") {
		t.Fatalf("raw panel did not keep the encrypted token source-exact:\n%s", plain)
	}
}

func TestItemRawPanelSanitizesMalformedTerminalText(t *testing.T) {
	raw := []byte("malformed\x1b[2J\u202erecord")
	item := newItemView(model.Event{
		Kind: model.EventSystem, RecordRef: writeRawRecord(t, raw),
	}, model.AgentCodex, nil, 40, 10, newStyles())
	cmd := item.requestRaw()
	item.update(cmd())

	view := item.view()
	lines := item.lines
	if len(lines) < 2 || lines[len(lines)-2].text != "Raw" || lines[len(lines)-1].text != `malformed\x1b[2J\u202erecord` {
		t.Fatalf("malformed raw did not render as one sanitized line: %#v", lines)
	}
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\u202e") || !strings.Contains(ansi.Strip(view), `malformed\x1b[2J\u202erecord`) {
		t.Fatalf("raw panel did not sanitize terminal text:\n%q", view)
	}
}

func TestTerminalSafeRawRecordEscapesOnlyUnsafeCharacters(t *testing.T) {
	raw := append([]byte(`safe \ path "界"`), '\t', '\n', '\r', 0, 0x1b, 0x7f)
	raw = append(raw, []byte("\u202e")...)
	raw = append(raw, 0xff)
	want := `safe \ path "界"\t\n\r\x00\x1b\x7f\u202e\xff`

	if got := terminalSafeRawRecord(raw); got != want {
		t.Fatalf("terminalSafeRawRecord() = %q, want %q", got, want)
	}
}

func TestTerminalSafeRawRecordDisambiguatesEscapedText(t *testing.T) {
	tests := []struct {
		name    string
		unsafe  []byte
		literal []byte
	}{
		{name: "tab", unsafe: []byte{'\t'}, literal: []byte(`\t`)},
		{name: "newline", unsafe: []byte{'\n'}, literal: []byte(`\n`)},
		{name: "escape", unsafe: []byte{0x1b}, literal: []byte(`\x1b`)},
		{name: "format rune", unsafe: []byte("\u202e"), literal: []byte(`\u202e`)},
		{name: "invalid UTF-8", unsafe: []byte{0xff}, literal: []byte(`\xff`)},
		{name: "backslash then tab", unsafe: []byte{'\\', '\t'}, literal: []byte(`\t`)},
		{name: "backslash then newline", unsafe: []byte{'\\', '\n'}, literal: []byte(`\n`)},
		{name: "backslash then escape", unsafe: []byte{'\\', 0x1b}, literal: []byte(`\x1b`)},
		{name: "backslash then format rune", unsafe: append([]byte{'\\'}, []byte("\u202e")...), literal: []byte(`\u202e`)},
		{name: "backslash then invalid UTF-8", unsafe: []byte{'\\', 0xff}, literal: []byte(`\xff`)},
		{name: "repeated backslash then newline", unsafe: []byte{'\\', '\\', '\n'}, literal: []byte(`\\n`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			escapedUnsafe := terminalSafeRawRecord(test.unsafe)
			escapedLiteral := terminalSafeRawRecord(test.literal)
			if escapedUnsafe == escapedLiteral {
				t.Fatalf("unsafe %q and literal %q both rendered as %q", test.unsafe, test.literal, escapedUnsafe)
			}
		})
	}
}

func TestItemToolLinesKeepResultSummarySeparateFromFullOutput(t *testing.T) {
	lines := itemEventLines(model.Event{
		Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check route",
		ResultSummary: "exit 0", Detail: &model.ToolDetail{Output: "route ready"},
	}, model.AgentCodex)
	var texts []string
	for _, line := range lines {
		texts = append(texts, line.text)
	}
	for _, want := range []string{"Output", "route ready", "Result summary", "exit 0"} {
		if !slices.Contains(texts, want) {
			t.Errorf("tool item missing %q: %#v", want, texts)
		}
	}
}

func TestItemToolBodyRendersReadableContent(t *testing.T) {
	lines := itemEventLines(model.Event{
		Kind: model.EventToolCall, ToolName: "exec", ToolInput: "make build",
		Detail: &model.ToolDetail{Input: "make build", Output: "build ready"},
	}, model.AgentCodex)
	roles := make(map[string]detailRole, len(lines))
	for _, line := range lines {
		roles[line.text] = line.role
	}
	for text, want := range map[string]detailRole{
		"Input": detailHeader, "make build": detailRow,
		"Output": detailHeader, "build ready": detailRow,
	} {
		if roles[text] != want {
			t.Errorf("item body %q role = %v, want %v", text, roles[text], want)
		}
	}
}

func TestItemTextRolesMatchTimelinePromptSemantics(t *testing.T) {
	for _, test := range []struct {
		kind model.EventKind
		want detailRole
	}{
		{kind: model.EventUser, want: detailUserPrompt},
		{kind: model.EventAssistantText, want: detailRow},
		{kind: model.EventThinking, want: detailSecondary},
		{kind: model.EventSystem, want: detailSystemPrompt},
		{kind: model.EventCompact, want: detailSystemPrompt},
		{kind: model.EventUsage, want: detailSystemPrompt},
	} {
		lines := itemEventLines(model.Event{Kind: test.kind, Text: "ordinary prose"}, model.AgentCodex)
		if len(lines) != 1 || lines[0].role != test.want {
			t.Errorf("item %s roles = %#v, want one role %v", test.kind, lines, test.want)
		}
	}
}

func TestItemLabelsMatchTimelineRoles(t *testing.T) {
	for _, test := range []struct {
		name      string
		event     model.Event
		wantLabel string
		wantRole  detailRole
	}{
		{name: "harness", event: model.Event{Kind: model.EventUser, Text: "Injected instructions", Harness: true}, wantLabel: "Harness", wantRole: detailSystemPrompt},
		{name: "human", event: model.Event{Kind: model.EventUser, Text: "Survey the crater"}, wantLabel: "User", wantRole: detailUserPrompt},
		{name: "usage", event: model.Event{Kind: model.EventUsage, Text: "unattributed usage"}, wantLabel: "Usage", wantRole: detailSystemPrompt},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := itemLabel(test.event, model.AgentClaude); got != test.wantLabel {
				t.Errorf("itemLabel() = %q, want %q", got, test.wantLabel)
			}
			lines := itemEventLines(test.event, model.AgentClaude)
			if len(lines) != 1 || lines[0].role != test.wantRole {
				t.Errorf("itemEventLines() = %#v, want one line with role %v", lines, test.wantRole)
			}
		})
	}
}

func TestItemContentSectionTitlesReflectKind(t *testing.T) {
	tests := []struct {
		event model.Event
		want  string
	}{
		{event: model.Event{Kind: model.EventAssistantText}, want: "Message"},
		{event: model.Event{Kind: model.EventUser}, want: "Prompt"},
		{event: model.Event{Kind: model.EventUser, Harness: true}, want: "Harness"},
		{event: model.Event{Kind: model.EventThinking}, want: "Thinking"},
		{event: model.Event{Kind: model.EventAdvisor}, want: "Advisor"},
		{event: model.Event{Kind: model.EventSystem}, want: "System"},
		{event: model.Event{Kind: model.EventCompact}, want: "Compact"},
		{event: model.Event{Kind: model.EventUsage}, want: "Usage"},
	}
	for _, test := range tests {
		test.event.Text = "section body"
		item := newItemView(test.event, model.AgentCodex, nil, 80, 12, newStyles())
		var headers []string
		for _, line := range item.lines {
			if line.role == detailHeader {
				headers = append(headers, line.text)
			}
		}
		if !slices.Contains(headers, test.want) {
			t.Errorf("%s item headers = %#v, want %q", test.event.Kind, headers, test.want)
		}
	}
}

func TestItemViewContentIsPlainTerminalTextAndWidthBounded(t *testing.T) {
	item := newItemView(model.Event{
		Kind: model.EventToolCall, ToolName: "Bash\x1b[2J", ToolInput: "fallback",
		Detail: &model.ToolDetail{
			Input:  "route\tinput\u202e\nsecond line",
			Diff:   "\x1b[31m-old\x1b[0m\n context\n\x1b[32m+new\x1b[0m",
			Output: "route\rready\x1b]8;;https://invalid.example\a link\x1b]8;;\a 航路🚀",
		},
	}, model.AgentClaude, []string{"starship\x1b[2J", "Plan\u202e route"}, 20, 10, newStyles())
	item.setWrap(true)

	for name, value := range map[string]string{
		"title": item.title(),
		"lines": func() string {
			lines := make([]string, len(item.lines))
			for index, line := range item.lines {
				lines[index] = line.text
			}
			return strings.Join(lines, "\n")
		}(),
		"view": item.view(),
	} {
		if strings.ContainsAny(value, "\r\t\x1b\a") || strings.ContainsRune(value, '\u202e') {
			t.Fatalf("item %s retained unsafe terminal data %q", name, value)
		}
	}
	for number, line := range strings.Split(item.view(), "\n") {
		if width := ansi.StringWidth(line); width > 20 {
			t.Fatalf("item line %d width = %d, want <= 20: %q", number+1, width, line)
		}
	}
}

func TestEnterOnTextRowOpensFullItemView(t *testing.T) {
	text := make([]string, detailPreviewLineCap+2)
	for index := range text {
		text[index] = fmt.Sprintf("observation-%02d", index)
	}
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude, Project: "starship", Title: "Survey ridge",
		Events: []model.Event{{Kind: model.EventAssistantText, Text: strings.Join(text, "\n")}},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 50},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyEnter},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}

	view := ansi.Strip(m.View())
	for _, want := range []string{"starship › Survey ridge › claude message", "observation-00", "observation-41"} {
		if !strings.Contains(view, want) {
			t.Fatalf("text item view missing %q:\n%s", want, view)
		}
	}
	if len(m.detailStack) != 1 {
		t.Fatalf("text open stack depth = %d, want 1", len(m.detailStack))
	}
}

func TestItemViewScrollsWithStepAndEdgeKeys(t *testing.T) {
	lines := make([]string, 12)
	for index := range lines {
		lines[index] = fmt.Sprintf("observation-%02d", index)
	}
	item := newItemView(model.Event{Kind: model.EventThinking, Text: strings.Join(lines, "\n")}, model.AgentClaude, nil, 40, 7, newStyles())

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if item.viewport.YOffset != 1 {
		t.Fatalf("j offset = %d, want 1", item.viewport.YOffset)
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if item.viewport.YOffset != 0 {
		t.Fatalf("k offset = %d, want 0", item.viewport.YOffset)
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if item.viewport.YOffset == 0 || !strings.Contains(ansi.Strip(item.view()), "observation-11") {
		t.Fatalf("G did not reveal the final item row at offset %d:\n%s", item.viewport.YOffset, ansi.Strip(item.view()))
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if item.viewport.YOffset != 0 || !strings.Contains(ansi.Strip(item.view()), "Event") {
		t.Fatalf("g did not restore the first item section at offset %d:\n%s", item.viewport.YOffset, ansi.Strip(item.view()))
	}
}

func TestItemKeyBarFollowsDetailKeyBarRules(t *testing.T) {
	event := model.Event{Kind: model.EventThinking, Text: "Chart route"}
	keyBar := func(item *itemView) string {
		lines := strings.Split(ansi.Strip(item.view()), "\n")
		return lines[len(lines)-1]
	}

	item := newItemView(event, model.AgentClaude, nil, 160, 8, newStyles(themes["default"]))
	if !item.wrap {
		t.Fatal("fixture item did not start wrapped")
	}
	bar := keyBar(item)
	for _, want := range []string{"w nowrap", "t theme", "? help", "q quit"} {
		if !strings.Contains(bar, want) {
			t.Errorf("wrapped item key bar missing %q: %q", want, bar)
		}
	}
	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if bar := keyBar(item); !strings.Contains(bar, "w wrap") || strings.Contains(bar, "w nowrap") {
		t.Errorf("unwrapped item key bar = %q, want the w wrap hint", bar)
	}

	mono := newItemView(event, model.AgentClaude, nil, 160, 8, newStyles(Theme{Name: "mono"}))
	if bar := keyBar(mono); strings.Contains(bar, "t theme") || !strings.Contains(bar, "? help") {
		t.Errorf("mono item key bar = %q, want help without the inert theme hint", bar)
	}
}

func TestItemViewWrapToggleRebuildsFlatPlainRows(t *testing.T) {
	text := strings.Repeat("charted route ", 12)
	item := newItemView(model.Event{Kind: model.EventAssistantText, Text: text}, model.AgentClaude, nil, 24, 8, newStyles())
	if !item.wrap || len(item.rendered) <= 1 {
		t.Fatalf("default item state = wrap %t rows %d, want wrapped rows", item.wrap, len(item.rendered))
	}
	for index, row := range item.rendered {
		if strings.Contains(row.text, "\x1b") || ansi.StringWidth(row.text) != item.viewport.Width {
			t.Fatalf("wrapped row %d = %q width %d, want plain width %d", index, row.text, ansi.StringWidth(row.text), item.viewport.Width)
		}
	}

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if item.wrap || len(item.rendered) != len(item.lines) {
		t.Fatalf("first wrap toggle state = wrap %t rows %d, want one row per source line (%d)", item.wrap, len(item.rendered), len(item.lines))
	}

	item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !item.wrap || len(item.rendered) <= 1 {
		t.Fatalf("second wrap toggle state = wrap %t rows %d, want wrapped rows", item.wrap, len(item.rendered))
	}
}

func TestDetailAndItemWrapByDefault(t *testing.T) {
	detail := newDetailState(&model.Session{ID: "lunar", Agent: model.AgentCodex}, 80, 12, newStyles())
	item := newItemView(model.Event{Kind: model.EventAssistantText, Text: "Route ready"}, model.AgentCodex, nil, 80, 12, newStyles())

	if !detail.wrap || !item.wrap {
		t.Fatalf("default wrap = detail %t, item %t; want both enabled", detail.wrap, item.wrap)
	}
}

func TestLoadedItemModelUpdateSharesImmutableRawRecord(t *testing.T) {
	item := newItemView(model.Event{Kind: model.EventSystem}, model.AgentCodex, nil, 80, 12, newStyles())
	item.raw = []byte(`{"route":"ready"}`)
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item

	updated, _ := m.Update(struct{}{})
	cloned := updated.(Model).detail.(*itemView)
	if &cloned.raw[0] != &item.raw[0] {
		t.Fatal("Model.Update copied an immutable loaded raw record")
	}
}

func TestItemViewLayoutChangesClampScrollOffset(t *testing.T) {
	lines := make([]string, 12)
	for index := range lines {
		lines[index] = fmt.Sprintf("observation-%02d %s", index, strings.Repeat("route ", 8))
	}

	t.Run("resize", func(t *testing.T) {
		item := newItemView(model.Event{Kind: model.EventThinking, Text: strings.Join(lines, "\n")}, model.AgentClaude, nil, 80, 7, newStyles())
		item.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		item.resize(80, 10)
		maxOffset := max(0, len(item.rendered)-item.viewport.Height)
		if item.viewport.YOffset > maxOffset {
			t.Fatalf("resized item offset = %d, want <= %d", item.viewport.YOffset, maxOffset)
		}
	})

	t.Run("disable wrap", func(t *testing.T) {
		item := newItemView(model.Event{Kind: model.EventThinking, Text: strings.Join(lines[:10], "\n")}, model.AgentClaude, nil, 30, 7, newStyles())
		item.setWrap(true)
		item.viewport.SetYOffset(8)
		item.setWrap(false)
		maxOffset := max(0, len(item.rendered)-item.viewport.Height)
		if item.viewport.YOffset > maxOffset {
			t.Fatalf("unwrapped item offset = %d, want <= %d", item.viewport.YOffset, maxOffset)
		}
	})
}

func BenchmarkItemRenderer16MiB(b *testing.B) {
	const envelope = `{"payload":""}`
	raw := []byte(`{"payload":"` + strings.Repeat("x", jsonl.MaxLineBytes-len(envelope)) + `"}`)
	item := newItemView(model.Event{
		Kind:      model.EventSystem,
		RecordRef: model.RecordRef{Path: "/fictional/session.jsonl", Length: int64(len(raw)), Digest: sha256.Sum256(raw)},
	}, model.AgentCodex, nil, 120, 24, newStyles())
	item.raw = raw
	item.rawLines = rawDetailLines(terminalSafePrettyRawRecordLines(raw), item.agent)
	item.rebuildLines()
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		item.rebuildLines()
		item.rebuild()
	}
}

func BenchmarkLoadedItemModelUpdate16MiB(b *testing.B) {
	raw := make([]byte, 16<<20)
	item := newItemView(model.Event{Kind: model.EventSystem}, model.AgentCodex, nil, 120, 24, newStyles())
	item.raw = raw
	m := NewModel(nil, nil)
	m.screen = screenDetail
	m.detail = item
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		updated, _ := m.Update(struct{}{})
		m = updated.(Model)
	}
}

func TestItemViewNarrowTitleKeepsItemLabel(t *testing.T) {
	crumbs := make([]string, 20)
	for index := range crumbs {
		crumbs[index] = fmt.Sprintf("fictional-observatory-%02d", index)
	}
	item := newItemView(
		model.Event{Kind: model.EventThinking, Text: "Chart route"},
		model.AgentClaude,
		crumbs,
		32,
		8,
		newStyles(),
	)
	title := item.title()
	if !strings.Contains(title, "Thinking") {
		t.Fatalf("narrow item title hid its label: %q", title)
	}
	if width := ansi.StringWidth(title); width > 27 {
		t.Fatalf("narrow item title width = %d, want <= 27: %q", width, title)
	}
}

func TestEscapePopsItemViewToDetail(t *testing.T) {
	session := &model.Session{
		ID: "lunar", Agent: model.AgentClaude,
		Events: []model.Event{{Kind: model.EventUser, Text: "Chart the route"}},
	}
	m := NewModel([]*model.Session{session}, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyEnter}, {Type: tea.KeyEsc}} {
		updated, _ := m.Update(key)
		m = updated.(Model)
	}

	if m.screen != screenDetail || len(m.detailStack) != 0 || detailStateFromScreen(t, m.detail).session != session {
		t.Fatalf("escape from item restored screen=%v detail=%T stack=%d, want root detail", m.screen, m.detail, len(m.detailStack))
	}
}

func TestOpenKeysPushNonSubagentItemView(t *testing.T) {
	for _, open := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
	} {
		t.Run(open.String(), func(t *testing.T) {
			session := &model.Session{
				ID: "lunar", Agent: model.AgentClaude,
				Events: []model.Event{{Kind: model.EventUser, Text: "Chart the route"}},
			}
			m := NewModel([]*model.Session{session}, nil)
			for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, open} {
				updated, _ := m.Update(key)
				m = updated.(Model)
			}

			if _, ok := m.detail.(*itemView); !ok || len(m.detailStack) != 1 {
				t.Fatalf("%q opened screen=%T stack=%d, want item over one parent", open.String(), m.detail, len(m.detailStack))
			}
		})
	}
}
