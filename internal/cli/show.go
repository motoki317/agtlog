package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source"
)

const machineResponseBudgetBytes = 256 << 10

type showOptions struct {
	common   commonOptions
	kind     string
	limit    int
	offset   int
	all      bool
	maxText  int
	full     bool
	noEvents bool
	raw      int
}

func runShow(ctx context.Context, args []string, help io.Writer, factory RegistryFactory) (any, string, error) {
	options, selector, err := parseShowOptions(args, help)
	if errorsIsHelp(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	registry, err := factory(ctx, options.common.registryOptions())
	if err != nil {
		return nil, "", runtimeError("internal", err.Error())
	}
	roots, diagnostics, err := registry.DiscoverWithDiagnostics(ctx)
	if err != nil {
		return nil, "", runtimeError("internal", err.Error())
	}
	_, nodes, commandDiags := addressableGraph(roots, commandDiagnostics(diagnostics))
	selected, err := resolveSelector(selector, nodes, commandDiags)
	if err != nil {
		return nil, "", err
	}
	if options.noEvents {
		response, buildErr := buildShowResponse(selected, nodes, options)
		return response, options.common.format, buildErr
	}
	if err := loadNodeDetail(ctx, registry, selected.session); err != nil {
		return nil, "", runtimeError("unreadable_session", "the selected session could not be read: "+err.Error())
	}
	if options.raw >= 0 {
		response, err := rawResponse(ctx, selected.session, options.raw)
		return response, options.common.format, err
	}
	if err := validateWireEventKinds(selected.session.Events); err != nil {
		return nil, "", runtimeError("internal", err.Error())
	}
	response, err := buildShowResponse(selected, nodes, options)
	return response, options.common.format, err
}

func validateWireEventKinds(events []model.Event) error {
	for _, event := range events {
		if _, valid := wireEventKind(event.Kind); !valid {
			return fmt.Errorf("event kind %q has no schema v1 mapping", event.Kind)
		}
	}
	return nil
}

func parseShowOptions(args []string, help io.Writer) (showOptions, string, error) {
	options := showOptions{limit: 200, maxText: 2000, raw: -1}
	flags := newFlagSet("agtlog show", help, showUsage)
	addCommonFlags(flags, &options.common)
	flags.StringVar(&options.kind, "kind", "", "comma-separated event kinds")
	flags.IntVar(&options.limit, "limit", 200, "maximum events to return")
	flags.IntVar(&options.offset, "offset", 0, "full-timeline index to start at")
	flags.BoolVar(&options.all, "all", false, "return every matching event")
	flags.IntVar(&options.maxText, "max-text", 2000, "maximum runes per text field; zero is unbounded")
	flags.BoolVar(&options.full, "full", false, "do not bound individual text fields")
	flags.BoolVar(&options.noEvents, "no-events", false, "return only the session summary")
	flags.IntVar(&options.raw, "raw", -1, "return the source record for an event index")
	operands, err := parseFlexible(flags, args, 1)
	if err != nil {
		return showOptions{}, "", err
	}
	if err := options.common.validate(); err != nil {
		return showOptions{}, "", err
	}
	if options.limit <= 0 {
		return showOptions{}, "", usageError("--limit must be greater than zero")
	}
	if options.offset < 0 {
		return showOptions{}, "", usageError("--offset must not be negative")
	}
	if options.maxText < 0 {
		return showOptions{}, "", usageError("--max-text must not be negative")
	}
	if options.raw < -1 {
		return showOptions{}, "", usageError("--raw must not be negative")
	}
	if options.raw >= 0 && options.common.format == "text" {
		return showOptions{}, "", usageError("--raw requires --format json")
	}
	if options.noEvents && options.raw >= 0 {
		return showOptions{}, "", usageError("--no-events and --raw cannot be used together")
	}
	if options.full {
		options.maxText = 0
	}
	if _, err := parseKinds(options.kind); err != nil {
		return showOptions{}, "", err
	}
	return options, operands[0], nil
}

func showUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: agtlog show <ref> [flags]")
	_, _ = fmt.Fprintln(output, "Shows one session timeline as indented JSON by default.")
	_, _ = fmt.Fprintln(output, "Flags can appear before or after <ref>.")
}

func parseKinds(value string) (map[model.EventKind]bool, error) {
	if value == "" {
		return nil, nil
	}
	result := make(map[model.EventKind]bool)
	for _, name := range strings.Split(value, ",") {
		kind := model.EventKind(name)
		if _, valid := wireEventKind(kind); !valid {
			return nil, usageError(fmt.Sprintf("invalid event kind %q", name))
		}
		result[kind] = true
	}
	return result, nil
}

func buildShowResponse(selected graphNode, nodes []graphNode, options showOptions) (ShowResponse, error) {
	refs := make([]string, 0, len(selected.session.Subagents))
	for _, child := range selected.session.Subagents {
		if ref := refForSession(nodes, child); ref != "" {
			refs = append(refs, ref)
		}
	}
	slices.Sort(refs)
	response := ShowResponse{
		SchemaVersion: SchemaVersion,
		Command:       "show",
		Session:       sessionDTO(selected.session, selected.ref),
		SubagentRefs:  nonNil(refs),
		Totals:        showTotals(selected.session),
		Events:        []Event{},
		Warnings:      []Warning{},
	}
	if options.noEvents {
		response.Page = ShowPage{Offset: options.offset, NextOffset: options.offset, Complete: true}
		if !showResponseFits(response) {
			return ShowResponse{}, runtimeError("internal", "session metadata exceeds the show response budget")
		}
		return response, nil
	}
	if !showResponseFits(response) {
		return ShowResponse{}, runtimeError("internal", "session metadata exceeds the show response budget")
	}
	kinds, _ := parseKinds(options.kind)
	total := 0
	for _, event := range selected.session.Events {
		if kinds == nil || kinds[event.Kind] {
			total++
		}
	}
	limit := options.limit
	if options.all {
		limit = 0
	}
	budgetStopped := false
	for index, event := range selected.session.Events {
		if index < options.offset || kinds != nil && !kinds[event.Kind] {
			continue
		}
		if limit > 0 && len(response.Events) >= limit {
			break
		}
		response.Events = append(response.Events, eventDTO(index, event, nodes, options.maxText))
		response.Page = ShowPage{Offset: options.offset, Limit: limit, Returned: len(response.Events), Total: total, NextOffset: index + 1}
		encoded, _ := json.MarshalIndent(response, "", "  ")
		if len(encoded)+1 > machineResponseBudgetBytes {
			if len(response.Events) == 1 {
				fitted, ok := fitSingleEvent(response, index, event, nodes)
				if !ok {
					return ShowResponse{}, runtimeError("internal", "event metadata exceeds the show response budget")
				}
				response.Events[0] = fitted
			} else {
				response.Events = response.Events[:len(response.Events)-1]
				budgetStopped = true
				break
			}
		}
	}
	next := options.offset
	if len(response.Events) > 0 {
		next = response.Events[len(response.Events)-1].Index + 1
	}
	hasMore := budgetStopped || matchingEventAtOrAfter(selected.session.Events, kinds, next)
	response.Page = ShowPage{
		Offset:     options.offset,
		Limit:      limit,
		Returned:   len(response.Events),
		Total:      total,
		HasMore:    hasMore,
		NextOffset: next,
		Complete:   !hasMore,
	}
	return response, nil
}

func showResponseFits(response ShowResponse) bool {
	encoded, _ := json.MarshalIndent(response, "", "  ")
	return len(encoded)+1 <= machineResponseBudgetBytes
}

func fitSingleEvent(response ShowResponse, index int, event model.Event, nodes []graphNode) (Event, bool) {
	maximum := maxTextRunes(event)
	best := eventDTO(index, event, nodes, 1)
	response.Events[0] = best
	if !showResponseFits(response) {
		return Event{}, false
	}
	for low, high := 1, maximum; low <= high; {
		limit := low + (high-low)/2
		candidate := eventDTO(index, event, nodes, limit)
		response.Events[0] = candidate
		encoded, _ := json.MarshalIndent(response, "", "  ")
		if len(encoded)+1 <= machineResponseBudgetBytes {
			best = candidate
			low = limit + 1
		} else {
			high = limit - 1
		}
	}
	return best, true
}

func maxTextRunes(event model.Event) int {
	projection := projectEventText(event)
	maximum := utf8.RuneCountInString(projection.text)
	maximum = max(maximum, utf8.RuneCountInString(projection.summary))
	maximum = max(maximum, utf8.RuneCountInString(projection.input))
	maximum = max(maximum, utf8.RuneCountInString(projection.diff))
	maximum = max(maximum, utf8.RuneCountInString(projection.output))
	return maximum
}

func matchingEventAtOrAfter(events []model.Event, kinds map[model.EventKind]bool, offset int) bool {
	for index := max(0, offset); index < len(events); index++ {
		if kinds == nil || kinds[events[index].Kind] {
			return true
		}
	}
	return false
}

func eventDTO(index int, event model.Event, nodes []graphNode, maxText int) Event {
	kind, _ := wireEventKind(event.Kind)
	projection := projectEventText(event)
	result := Event{
		Index:     index,
		Timestamp: timestamp(event.Timestamp),
		Kind:      kind,
		Text:      projection.text,
		Model:     event.Model,
		Truncated: []string{},
		Harness:   event.Harness,
	}
	result.Text = boundedField(result.Text, maxText, fieldText, &result.Truncated)
	if event.ToolName != "" || event.CallID != "" || event.Detail != nil || event.ResultSummary != "" {
		tool := &EventTool{Name: event.ToolName, CallID: event.CallID, Summary: projection.summary, Input: projection.input, Diff: projection.diff, Output: projection.output, DurationMS: event.Duration.Milliseconds()}
		tool.Summary = boundedField(tool.Summary, maxText, fieldToolSummary, &result.Truncated)
		tool.Input = boundedField(tool.Input, maxText, fieldToolInput, &result.Truncated)
		tool.Diff = boundedField(tool.Diff, maxText, fieldToolDiff, &result.Truncated)
		tool.Output = boundedField(tool.Output, maxText, fieldToolOutput, &result.Truncated)
		result.Tool = tool
	}
	if event.Usage != nil {
		totals := tokenTotals(*event.Usage)
		result.Usage = &EventUsage{
			UncachedInput: totals.UncachedInput,
			Output:        totals.Output,
			CacheWrite:    totals.CacheWrite,
			CacheRead:     totals.CacheRead,
			Flow:          event.Usage.FlowTokens(),
			Context:       event.Usage.PromptTokens(),
		}
		result.Cost = &EventCost{USD: event.Cost.Total(), Complete: event.Priced, Estimated: event.CostEstimated}
	}
	if event.RecordRef.Path != "" && event.RecordRef.Length > 0 {
		result.Record = &EventRecord{Path: event.RecordRef.Path, Offset: event.RecordRef.Offset, Length: event.RecordRef.Length}
	}
	if event.Subagent != nil {
		result.SubagentRef = refForSession(nodes, event.Subagent)
	}
	if event.Kind == model.EventCompact {
		result.Compact = &Compact{Trigger: event.CompactTrigger, PostTokens: event.CompactPostTokens}
	}
	return result
}

type eventTextProjection struct {
	text    string
	input   string
	diff    string
	output  string
	summary string
}

func projectEventText(event model.Event) eventTextProjection {
	projection := eventTextProjection{
		text:    event.Text,
		input:   event.ToolInput,
		summary: event.ResultSummary,
	}
	if event.Detail != nil {
		if event.Detail.Input != "" {
			projection.input = event.Detail.Input
		}
		projection.diff, projection.output = event.Detail.Diff, event.Detail.Output
	}
	if projection.text != "" {
		return projection
	}
	if event.ToolName == "" {
		return projection
	}
	if event.ToolInput == "" {
		projection.text = event.ToolName
		return projection
	}
	projection.text = event.ToolName + "(" + event.ToolInput + ")"
	return projection
}

func boundedField(value string, limit int, name string, truncated *[]string) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	*truncated = append(*truncated, name)
	return model.BoundedDetailText(value, limit)
}

func showTotals(session *model.Session) ShowTotals {
	selfUsage := session.OwnedSelfUsage()
	descendantUsage := session.OwnedDescendantUsage()
	selfCost := session.OwnedSelfCost()
	descendantCost := session.OwnedDescendantCost()
	return ShowTotals{
		Tokens: ScopedTokens{Self: tokenTotals(selfUsage), Descendants: tokenTotals(descendantUsage), Total: tokenTotals(session.OwnedUsage())},
		Cost:   ScopedCost{Self: costDTO(selfCost), Descendants: costDTO(descendantCost), Total: costDTO(session.OwnedCost())},
	}
}

func rawResponse(ctx context.Context, session *model.Session, index int) (RawResponse, error) {
	if index < 0 || index >= len(session.Events) {
		return RawResponse{}, resolutionError("not_found", "no event exists at the requested index", nil)
	}
	ref := session.Events[index].RecordRef
	if ref.Path == "" || ref.Length <= 0 {
		return RawResponse{}, runtimeError("record_unavailable", "the event has no source record")
	}
	raw, err := source.ReadRecord(ctx, ref)
	if err != nil {
		if errors.Is(err, source.ErrRecordChanged) || errors.Is(err, source.ErrRecordRead) {
			return RawResponse{}, runtimeError("record_changed", "the source record changed after discovery")
		}
		return RawResponse{}, runtimeError("internal", err.Error())
	}
	return RawResponse{
		SchemaVersion: SchemaVersion,
		Command:       "show",
		RawRecord:     RawRecord{Index: index, Path: ref.Path, Offset: ref.Offset, Length: ref.Length, RawJSON: string(raw)},
		Warnings:      []Warning{},
	}, nil
}
