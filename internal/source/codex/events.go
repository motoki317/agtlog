package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

type codexTextBlock struct {
	Text string `json:"text"`
}

func (p Parser) loadEvents(ctx context.Context, session *model.Session, recursive bool) error {
	clearCodexEvents(session)
	return p.loadEventsRecursive(ctx, session, 0, make(map[string]bool), recursive)
}

func (p Parser) loadEventsRecursive(ctx context.Context, session *model.Session, depth int, visited map[string]bool, recursive bool) error {
	if depth > maxAgentDepth {
		return fmt.Errorf("subagent nesting exceeds %d levels", maxAgentDepth)
	}
	path := strings.SplitN(session.Path, "#", 2)[0]
	if visited[path] {
		return fmt.Errorf("subagent event cycle at %q", path)
	}
	visited[path] = true
	defer delete(visited, path)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("session path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	type pendingCall struct {
		eventIndex          int
		normalizeExecOutput bool
		applyPatch          bool
	}
	type pendingUsage struct {
		event model.Event
		usage model.Usage
	}
	calls := make(map[string]pendingCall)
	dedupTextByEvent := make(map[int][32]byte)
	requestsByOffset := make(map[int64]model.RequestUsage, len(session.Requests))
	for _, request := range session.Requests {
		if request.Offset >= 0 {
			requestsByOffset[request.Offset] = request
		}
	}
	currentModel := ""
	candidateIndex := -1
	isSubagent := session.AgentPath != ""
	waitingForBridge := isSubagent
	initialEventLengths := codexEventLengths(session)
	var preBridgeUsage []pendingUsage
	var reader io.Reader = file
	if session.SourceSize > 0 {
		reader = io.LimitReader(file, session.SourceSize)
	}
	err = jsonl.ForEachContextWithOffset(ctx, reader, func(line []byte, offset, length int64) {
		var record struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type         string           `json:"type"`
				Role         string           `json:"role"`
				Model        string           `json:"model"`
				Message      string           `json:"message"`
				CallID       string           `json:"call_id"`
				Name         string           `json:"name"`
				Arguments    string           `json:"arguments"`
				Input        string           `json:"input"`
				Output       json.RawMessage  `json:"output"`
				AgentPath    string           `json:"agent_path"`
				AgentID      string           `json:"agent_thread_id"`
				Kind         string           `json:"kind"`
				ThreadSource string           `json:"thread_source"`
				Content      []codexTextBlock `json:"content"`
				// Codex reuses "summary" across payload variants with different
				// shapes: an array of blocks under reasoning, a bare string under
				// turn_context. Decoding it lazily keeps a turn_context line from
				// failing the whole record, which would lose the model and
				// mis-price the session. encoding/json does not guarantee the
				// remaining fields are filled after a type error, so the shape
				// must not conflict in the first place.
				Summary json.RawMessage `json:"summary"`
			} `json:"payload"`
		}
		if jsonl.Unmarshal(line, &record) != nil {
			return
		}
		recordRef := model.RecordRef{Path: path, Offset: offset, Length: length, Digest: sha256.Sum256(line)}
		timestamp, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
		if record.Type == "session_meta" {
			if record.Payload.ThreadSource == "subagent" {
				isSubagent, waitingForBridge = true, true
			}
			return
		}
		if record.Type == "inter_agent_communication_metadata" {
			if isSubagent && waitingForBridge {
				restoreCodexEventLengths(initialEventLengths)
				for _, pending := range preBridgeUsage {
					p.appendCodexUsageEvent(session, pending.event, pending.usage)
				}
				initialEventLengths = nil
				preBridgeUsage = nil
				calls = make(map[string]pendingCall)
				dedupTextByEvent = make(map[int][32]byte)
				candidateIndex = -1
				waitingForBridge = false
			}
			return
		}
		if record.Type == "turn_context" {
			currentModel = record.Payload.Model
			return
		}
		event := model.Event{Timestamp: timestamp, Model: currentModel, RecordRef: recordRef}
		eventCount := len(session.Events)
		preferredMessage := true
		if record.Type == "event_msg" {
			switch record.Payload.Type {
			case "user_message":
				event.Kind, event.Text = model.EventUser, codexTimelineUserMessage(record.Payload.Message)
				if event.Text == "" {
					return
				}
			case "agent_message":
				event.Kind, event.Text = model.EventAssistantText, record.Payload.Message
				if event.Text == "" {
					return
				}
			case "sub_agent_activity":
				appendCodexSubagentEvent(session, record.Payload.AgentPath, record.Payload.AgentID, record.Payload.Kind, timestamp, recordRef)
				return
			case "context_compacted":
				session.Events = append(session.Events, model.Event{Timestamp: timestamp, Kind: model.EventCompact, Text: "context compacted", RecordRef: recordRef, Model: currentModel})
				return
			case "token_count":
				if request, ok := requestsByOffset[offset]; ok {
					delete(requestsByOffset, offset)
					usage := request.Usage
					if waitingForBridge {
						preBridgeUsage = append(preBridgeUsage, pendingUsage{event: model.Event{
							Timestamp: timestamp,
							Kind:      model.EventUsage,
							Text:      "unattributed usage",
							RecordRef: recordRef,
							Model:     usage.Model,
						}, usage: usage})
					}
					if candidateIndex >= 0 {
						p.setCodexEventUsage(&session.Events[candidateIndex], usage)
					} else {
						p.appendCodexUsageEvent(session, model.Event{
							Timestamp: timestamp,
							Kind:      model.EventUsage,
							Text:      "unattributed usage",
							RecordRef: recordRef,
							Model:     usage.Model,
						}, usage)
					}
				}
				candidateIndex = -1
				return
			default:
				return
			}
			preferredMessage = false
		} else {
			if record.Type != "response_item" {
				return
			}
			switch record.Payload.Type {
			case "message", "agent_message":
				event.Text = joinCodexText(record.Payload.Content)
				if strings.HasPrefix(event.Text, "Message Type:") {
					if !isSubagent {
						return
					}
					event.Kind, event.Text = model.EventUser, codexDelegatedPayload(event.Text)
				} else if record.Payload.Role == "user" {
					event.Kind = model.EventUser
				} else if record.Payload.Role == "assistant" || record.Payload.Type == "agent_message" {
					event.Kind = model.EventAssistantText
				} else {
					return
				}
				if event.Kind == model.EventUser {
					event.Harness = true
					event.Text = codexTimelineUserMessage(event.Text)
				}
				if event.Text == "" {
					return
				}
			case "reasoning":
				event.Kind, event.Text = model.EventThinking, joinCodexText(codexTextBlocks(record.Payload.Summary))
			case "function_call", "custom_tool_call":
				event.Kind, event.CallID, event.ToolName = model.EventToolCall, record.Payload.CallID, record.Payload.Name
				input := record.Payload.Arguments
				if input == "" {
					input = record.Payload.Input
				}
				input = codexElideEncrypted(input)
				event.ToolInput, event.Detail = codexToolPresentation(record.Payload.Name, input)
				if record.Payload.Name == "exec" {
					if name, _, ok := codexExecTool(input); ok {
						event.ToolName = name
					}
				}
				calls[event.CallID] = pendingCall{
					eventIndex:          len(session.Events),
					normalizeExecOutput: record.Payload.Name == "exec" || record.Payload.Name == "wait",
					applyPatch:          event.ToolName == "apply_patch",
				}
			case "function_call_output", "custom_tool_call_output":
				event.Kind, event.CallID = model.EventToolResult, record.Payload.CallID
				output := codexElideEncrypted(codexOutputText(record.Payload.Output))
				pending, linked := calls[event.CallID]
				summary := ""
				if linked {
					if pending.normalizeExecOutput {
						var exitCode string
						output, exitCode = codexNormalizeExecOutput(output)
						if exitCode != "" {
							summary = "exit " + exitCode
						}
					}
				}
				if summary == "" {
					summary = codexResultSummary(output)
				}
				// An apply_patch result is exit-code and wall-time boilerplate that never
				// names what changed. Summarize it by the files the patch touches instead.
				if linked && pending.applyPatch {
					if call := &session.Events[pending.eventIndex]; call.Detail != nil {
						if files := codexPatchFiles(call.Detail.Diff); len(files) > 0 {
							summary = strings.Join(files, ", ")
						}
					}
				}
				event.Text = codexElideEncrypted(summary)
				if linked {
					call := &session.Events[pending.eventIndex]
					if call.Detail != nil && event.CallID != "" {
						call.Detail.Output = codexElideEncrypted(output)
					}
					call.ResultSummary = event.Text
					if !timestamp.Before(call.Timestamp) {
						call.Duration = timestamp.Sub(call.Timestamp)
					}
				}
			default:
				return
			}
		}
		dedupText := event.Text
		if event.Kind == model.EventUser || event.Kind == model.EventAssistantText || event.Kind == model.EventThinking {
			event.Text = codexElideEncrypted(event.Text)
		}
		event.ToolInput = codexElideEncrypted(event.ToolInput)
		if event.Kind == model.EventUser || event.Kind == model.EventAssistantText {
			if event.Text != "" {
				appendCodexMessage(session, event, preferredMessage, dedupText, dedupTextByEvent)
			}
		} else {
			event.Text = codexElideEncrypted(event.Text)
			if event.Text != "" || event.Kind == model.EventToolCall || event.Kind == model.EventToolResult {
				session.Events = append(session.Events, event)
			}
		}
		if candidateIndex < 0 {
			for index := eventCount; index < len(session.Events); index++ {
				if codexUsageTarget(session.Events[index].Kind) {
					candidateIndex = index
					break
				}
			}
		}
	})
	if err != nil {
		return err
	}
	for _, request := range session.Requests {
		if request.Offset >= 0 {
			if _, unmatched := requestsByOffset[request.Offset]; !unmatched {
				continue
			}
			p.appendCodexUsageEvent(session, model.Event{
				Timestamp: session.UpdatedAt,
				Kind:      model.EventUsage,
				Text:      "unattributed usage",
				RecordRef: model.RecordRef{Path: path, Offset: request.Offset},
				Model:     request.Usage.Model,
			}, request.Usage)
		} else {
			p.appendCodexUsageEvent(session, model.Event{
				Timestamp:      session.UpdatedAt,
				Kind:           model.EventUsage,
				Text:           "session usage",
				Model:          request.Usage.Model,
				UsageAggregate: true,
			}, request.Usage)
		}
	}
	if recursive {
		for _, subagent := range session.Subagents {
			if subagent.Path == "" || strings.Contains(subagent.Path, "#") {
				continue
			}
			if err := p.loadEventsRecursive(ctx, subagent, depth+1, visited, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func codexEventLengths(root *model.Session) map[*model.Session]int {
	lengths := make(map[*model.Session]int)
	var visit func(*model.Session)
	visit = func(session *model.Session) {
		if session == nil {
			return
		}
		if _, seen := lengths[session]; seen {
			return
		}
		lengths[session] = len(session.Events)
		for _, child := range session.Subagents {
			visit(child)
		}
	}
	visit(root)
	return lengths
}

func restoreCodexEventLengths(lengths map[*model.Session]int) {
	for session, length := range lengths {
		if len(session.Events) > length {
			if length == 0 {
				clear(session.Events)
				session.Events = nil
				continue
			}
			retained := slices.Clone(session.Events[:length])
			clear(session.Events)
			session.Events = retained
		}
	}
}

func (p Parser) appendCodexUsageEvent(session *model.Session, event model.Event, usage model.Usage) {
	session.Events = append(session.Events, p.codexUsageEvent(event, usage))
}

func (p Parser) codexUsageEvent(event model.Event, usage model.Usage) model.Event {
	p.setCodexEventUsage(&event, usage)
	return event
}

func codexUsageTarget(kind model.EventKind) bool {
	switch kind {
	case model.EventAssistantText, model.EventThinking, model.EventToolCall, model.EventAdvisor:
		return true
	default:
		return false
	}
}

func (p Parser) setCodexEventUsage(event *model.Event, usage model.Usage) {
	event.Usage = &usage
	calculated := p.calculator.CalculateCodex(usage, p.defaultPricingModel)
	event.Cost = p.calculator.BreakdownCodex(usage, p.defaultPricingModel)
	event.Priced = p.calculator.HasCodexPricing(usage, p.defaultPricingModel)
	event.CostEstimated = calculated.Estimated
	event.PricingModel = ""
	if len(calculated.EstimatedRates) > 0 {
		event.PricingModel = calculated.EstimatedRates[0].PricingModel
	}
}

func appendCodexMessage(session *model.Session, event model.Event, preferred bool, dedupText string, dedupTextByEvent map[int][32]byte) {
	event.Text = codexElideEncrypted(model.CleanTimelineText(event.Text))
	dedupKey := sha256.Sum256([]byte(model.CleanTimelineText(dedupText)))
	if event.Text == "" {
		return
	}
	// A 16-event window is the deduplication ceiling for mirrored message copies.
	for index := range dedupTextByEvent {
		if index < len(session.Events)-16 {
			delete(dedupTextByEvent, index)
		}
	}
	for index := len(session.Events) - 1; index >= 0 && index >= len(session.Events)-16; index-- {
		existing := session.Events[index]
		if existing.Kind != event.Kind {
			continue
		}
		existingKey, ok := dedupTextByEvent[index]
		if !ok {
			existingKey = sha256.Sum256([]byte(model.CleanTimelineText(existing.Text)))
		}
		if existingKey != dedupKey {
			continue
		}
		if preferred {
			if event.Kind == model.EventUser {
				event.Harness = event.Harness && existing.Harness
			}
			event.Usage = existing.Usage
			event.Cost = existing.Cost
			event.Priced = existing.Priced
			event.CostEstimated = existing.CostEstimated
			event.PricingModel = existing.PricingModel
			session.Events[index] = event
			if dedupTextByEvent != nil {
				dedupTextByEvent[index] = dedupKey
			}
		}
		return
	}
	index := len(session.Events)
	session.Events = append(session.Events, event)
	if dedupTextByEvent != nil {
		dedupTextByEvent[index] = dedupKey
	}
}

func codexDelegatedPayload(text string) string {
	const marker = "\nPayload:\n"
	if index := strings.Index(text, marker); index >= 0 {
		return strings.TrimSpace(text[index+len(marker):])
	}
	return text
}

func clearCodexEvents(session *model.Session) {
	session.Events = nil
	for _, subagent := range session.Subagents {
		clearCodexEvents(subagent)
	}
}

func appendCodexSubagentEvent(root *model.Session, agentPath, agentID, activity string, timestamp time.Time, recordRef model.RecordRef) {
	parts := agentPathParts(agentPath)
	base := agentPathParts(root.AgentPath)
	if len(base) > 0 && len(parts) > len(base) {
		matches := true
		for index := range base {
			matches = matches && base[index] == parts[index]
		}
		if matches {
			parts = parts[len(base):]
		}
	}
	if len(parts) == 0 {
		return
	}
	parent := root
	for index, name := range parts {
		var child *model.Session
		for _, candidate := range parent.Subagents {
			if candidate.Title == name || candidate.ID == name || (index == len(parts)-1 && candidate.ID == agentID) {
				child = candidate
				break
			}
		}
		if child == nil {
			return
		}
		if index == len(parts)-1 {
			if activity == "started" {
				parent.Events = append(parent.Events, model.Event{Timestamp: timestamp, Kind: model.EventSubagent, RecordRef: recordRef, AgentID: agentID, Subagent: child})
			} else {
				child.Events = append(child.Events, model.Event{Timestamp: timestamp, Kind: model.EventSystem, Text: activity, RecordRef: recordRef, AgentID: agentID})
			}
			return
		}
		parent = child
	}
}

func codexTextBlocks(raw json.RawMessage) []codexTextBlock {
	var blocks []codexTextBlock
	if jsonl.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

func joinCodexText(blocks []codexTextBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block.Text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func codexElideEncrypted(text string) string {
	return model.ElideEncrypted(text)
}

func codexOutputText(output json.RawMessage) string {
	raw := bytes.TrimSpace(output)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if jsonl.Unmarshal(raw, &text) != nil {
			return strings.TrimSpace(string(output))
		}
		return text
	}
	if raw[0] == '[' {
		var blocks []codexTextBlock
		if jsonl.Unmarshal(raw, &blocks) == nil {
			var text strings.Builder
			for index, block := range blocks {
				if index > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(block.Text)
			}
			return text.String()
		}
	}
	return strings.TrimSpace(string(output))
}

func codexToolInput(name, input string) string {
	if name == "exec" {
		toolInput, _ := codexExecToolPresentation(input)
		return toolInput
	}
	if name == "apply_patch" {
		added, removed := 0, 0
		for _, line := range strings.Split(input, "\n") {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				added++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				removed++
			}
		}
		return fmt.Sprintf("+%d −%d", added, removed)
	}
	var fields map[string]json.RawMessage
	if jsonl.Unmarshal([]byte(input), &fields) == nil {
		key := ""
		switch name {
		case "exec_command":
			key = "cmd"
		case "view_image", "Read", "Edit", "Write":
			key = "path"
		}
		if key != "" {
			var value string
			if jsonl.Unmarshal(fields[key], &value) == nil {
				return value
			}
		}
	}
	return strings.Join(strings.Fields(input), " ")
}

func codexToolDetail(name, input string) *model.ToolDetail {
	if name == "exec" {
		_, detail := codexExecToolPresentation(input)
		return detail
	}
	detail := &model.ToolDetail{}
	switch name {
	case "apply_patch":
		detail.Diff = codexElideEncrypted(input)
	case "exec_command":
		var fields struct {
			Command string `json:"cmd"`
		}
		if jsonl.Unmarshal([]byte(input), &fields) == nil {
			detail.Input = codexElideEncrypted(fields.Command)
		} else {
			detail.Input = codexElideEncrypted(input)
		}
	default:
		detail.Input = codexElideEncrypted(codexPrettyInput(input))
	}
	return detail
}

func codexToolPresentation(name, input string) (string, *model.ToolDetail) {
	if name == "exec" {
		return codexExecToolPresentation(input)
	}
	return codexToolInput(name, input), codexToolDetail(name, input)
}

func codexExecToolPresentation(input string) (string, *model.ToolDetail) {
	calls := codexExecTools(input)
	if len(calls) > 1 {
		return "", &model.ToolDetail{Input: codexElideEncrypted(codexPrettyInput(input))}
	}
	name, _, wrapped := codexExecTool(input)
	if wrapped && name == "apply_patch" {
		if patch, ok := codexApplyPatchBody(input); ok {
			return codexToolInput("apply_patch", patch), codexToolDetail("apply_patch", patch)
		}
	}
	if wrapped && name != "exec_command" {
		return "", &model.ToolDetail{Input: codexElideEncrypted(codexPrettyInput(input))}
	}
	command, ok := codexExecCommand(input)
	if !ok {
		return strings.Join(strings.Fields(input), " "), &model.ToolDetail{Input: codexElideEncrypted(codexPrettyInput(input))}
	}
	return strings.SplitN(command, "\n", 2)[0], &model.ToolDetail{Input: codexElideEncrypted(command)}
}

// codexPatchFiles returns the basenames of the files a Codex apply_patch envelope
// touches, in first-seen order. Each file section begins with an "*** Update File:",
// "*** Add File:", or "*** Delete File:" header; a rename adds a "*** Move to:" line
// that names the same file again, so only the section headers are collected. The
// names summarize the edit for the timeline in place of the tool's exit-code and
// wall-time output.
func codexPatchFiles(patch string) []string {
	prefixes := []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "}
	var files []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range prefixes {
			raw, ok := strings.CutPrefix(line, prefix)
			if !ok {
				continue
			}
			if name := path.Base(strings.TrimSpace(raw)); name != "" && name != "." && name != "/" && !seen[name] {
				seen[name] = true
				files = append(files, name)
			}
			break
		}
	}
	return files
}

// codexApplyPatchBegin marks the start of an apply_patch envelope.
const codexApplyPatchBegin = "*** Begin Patch"

// codexApplyPatchBody returns the decoded patch envelope handed to a wrapped
// tools.apply_patch call. The wrapper assigns the patch to a JS string literal —
// often a variable referenced by the call rather than an inline argument — so the
// literal is located by its envelope marker instead of by the call's argument.
func codexApplyPatchBody(input string) (string, bool) {
	for index := 0; index < len(input); {
		switch input[index] {
		case '\'', '"', '`':
			if decoded, _, valid := codexJSQuotedString(input, index); valid && strings.Contains(decoded, codexApplyPatchBegin) {
				return decoded, true
			}
			if next := codexSkipJSQuoted(input, index); next > index {
				index = next
				continue
			}
		}
		index++
	}
	return "", false
}

func codexExecCommand(input string) (string, bool) {
	name, argumentsStart, ok := codexExecTool(input)
	if !ok || name != "exec_command" {
		return "", false
	}
	objectStart := argumentsStart
	for objectStart < len(input) && strings.ContainsRune(" \t\r\n", rune(input[objectStart])) {
		objectStart++
	}
	if objectStart == len(input) || input[objectStart] != '{' {
		return "", false
	}
	command := ""
	objectEnd, valid := codexVisitJSObjectMembers(input, objectStart, func(member string) bool {
		colon := strings.IndexByte(member, ':')
		if colon < 0 || strings.TrimSpace(member[:colon]) == "" || strings.TrimSpace(member[colon+1:]) == "" {
			return false
		}
		key := strings.TrimSpace(member[:colon])
		key, valid := codexJSObjectKey(key)
		if !valid {
			return false
		}
		value := strings.TrimSpace(member[colon+1:])
		if key != "cmd" {
			return codexJSStaticObjectValue(value)
		}
		if command != "" {
			return false
		}
		decoded, end, valid := codexJSQuotedString(value, 0)
		if !valid || strings.TrimSpace(value[end:]) != "" || value[0] == '`' && codexJSTemplateInterpolates(value[:end]) || decoded == "" {
			return false
		}
		command = decoded
		return true
	})
	if !valid || command == "" {
		return "", false
	}
	for objectEnd < len(input) && strings.ContainsRune(" \t\r\n", rune(input[objectEnd])) {
		objectEnd++
	}
	return command, objectEnd < len(input) && input[objectEnd] == ')'
}

func codexJSObjectKey(key string) (string, bool) {
	if strings.ContainsRune("'\"`", rune(key[0])) {
		decoded, end, valid := codexJSQuotedString(key, 0)
		return decoded, valid && end == len(key)
	}
	for index := range len(key) {
		if !codexJSIdentifierPart(key[index]) && key[index] != '-' {
			return "", false
		}
	}
	return key, true
}

func codexJSStaticObjectValue(value string) bool {
	if strings.ContainsRune("'\"`", rune(value[0])) {
		_, end, valid := codexJSQuotedString(value, 0)
		return valid && end == len(value) && (value[0] != '`' || !codexJSTemplateInterpolates(value))
	}
	if value[0] == '{' && value[len(value)-1] == '}' || value[0] == '[' && value[len(value)-1] == ']' {
		return true
	}
	if value == "true" || value == "false" || value == "null" {
		return true
	}
	_, err := strconv.ParseFloat(value, 64)
	return err == nil
}

func codexVisitJSObjectMembers(input string, objectStart int, visit func(string) bool) (int, bool) {
	curlyDepth, squareDepth, parenDepth := 1, 0, 0
	memberStart := objectStart + 1
	for index := memberStart; index < len(input); {
		switch input[index] {
		case '\'', '"', '`':
			index = codexSkipJSQuoted(input, index)
			continue
		case '/':
			if index+1 < len(input) && input[index+1] == '/' {
				if end := strings.IndexByte(input[index+2:], '\n'); end >= 0 {
					index += end + 3
					continue
				}
				return 0, false
			}
			if index+1 < len(input) && input[index+1] == '*' {
				if end := strings.Index(input[index+2:], "*/"); end >= 0 {
					index += end + 4
					continue
				}
				return 0, false
			}
		case '{':
			curlyDepth++
		case '}':
			curlyDepth--
			if curlyDepth == 0 {
				if squareDepth != 0 || parenDepth != 0 {
					return 0, false
				}
				if member := strings.TrimSpace(input[memberStart:index]); member != "" {
					return index + 1, visit(member)
				}
				return index + 1, true
			}
		case '[':
			squareDepth++
		case ']':
			squareDepth--
			if squareDepth < 0 {
				return 0, false
			}
		case '(':
			parenDepth++
		case ')':
			parenDepth--
			if parenDepth < 0 {
				return 0, false
			}
		case ',':
			if curlyDepth == 1 && squareDepth == 0 && parenDepth == 0 {
				member := strings.TrimSpace(input[memberStart:index])
				if member == "" || !visit(member) {
					return 0, false
				}
				memberStart = index + 1
			}
		}
		index++
	}
	return 0, false
}

func codexJSTemplateInterpolates(value string) bool {
	for index := 1; index < len(value)-1; index++ {
		if value[index] == '\\' {
			index++
			continue
		}
		if value[index] == '$' && index+1 < len(value)-1 && value[index+1] == '{' {
			return true
		}
	}
	return false
}

func codexJSQuotedString(input string, start int) (string, int, bool) {
	if start >= len(input) || !strings.ContainsRune("'\"`", rune(input[start])) {
		return "", start, false
	}
	quote := input[start]
	end := codexSkipJSQuoted(input, start)
	if end <= start+1 || end > len(input) || input[end-1] != quote {
		return "", end, false
	}
	var decoded strings.Builder
	decoded.Grow(end - start - 2)
	for index := start + 1; index < end-1; {
		if input[index] != '\\' {
			if quote != '`' && (input[index] == '\n' || input[index] == '\r') {
				return "", end, false
			}
			decoded.WriteByte(input[index])
			index++
			continue
		}
		if index+1 >= end-1 {
			return "", end, false
		}
		escaped := input[index+1]
		switch escaped {
		case '\\', '\'', '"', '`':
			decoded.WriteByte(escaped)
			index += 2
		case 'b':
			decoded.WriteByte('\b')
			index += 2
		case 'f':
			decoded.WriteByte('\f')
			index += 2
		case 'n':
			decoded.WriteByte('\n')
			index += 2
		case 'r':
			decoded.WriteByte('\r')
			index += 2
		case 't':
			decoded.WriteByte('\t')
			index += 2
		case 'v':
			decoded.WriteByte('\v')
			index += 2
		case '0':
			decoded.WriteByte(0)
			index += 2
		case '\n':
			index += 2
		case '\r':
			index += 2
			if index < end-1 && input[index] == '\n' {
				index++
			}
		case 'x':
			value, next, ok := codexJSHexEscape(input, index+2, 2, end-1)
			if !ok {
				return "", end, false
			}
			decoded.WriteRune(value)
			index = next
		case 'u':
			value, next, ok := codexJSUnicodeEscape(input, index+2, end-1)
			if !ok {
				return "", end, false
			}
			decoded.WriteRune(value)
			index = next
		default:
			decoded.WriteByte(escaped)
			index += 2
		}
	}
	return decoded.String(), end, true
}

func codexJSHexEscape(input string, start, width, limit int) (rune, int, bool) {
	if start+width > limit {
		return 0, start, false
	}
	value, err := strconv.ParseUint(input[start:start+width], 16, 32)
	return rune(value), start + width, err == nil
}

func codexJSUnicodeEscape(input string, start, limit int) (rune, int, bool) {
	if start < limit && input[start] == '{' {
		end := strings.IndexByte(input[start+1:limit], '}')
		if end < 0 || end == 0 || end > 6 {
			return 0, start, false
		}
		value, err := strconv.ParseUint(input[start+1:start+1+end], 16, 32)
		if err != nil || value > unicode.MaxRune || value >= 0xd800 && value <= 0xdfff {
			return 0, start, false
		}
		return rune(value), start + end + 2, true
	}
	value, next, ok := codexJSHexEscape(input, start, 4, limit)
	if !ok {
		return 0, start, false
	}
	if value >= 0xd800 && value <= 0xdbff {
		if next+2 > limit || input[next] != '\\' || input[next+1] != 'u' {
			return utf8.RuneError, next, true
		}
		low, lowNext, valid := codexJSHexEscape(input, next+2, 4, limit)
		if !valid || low < 0xdc00 || low > 0xdfff {
			return utf8.RuneError, next, true
		}
		return 0x10000 + (value-0xd800)<<10 + low - 0xdc00, lowNext, true
	}
	if value >= 0xdc00 && value <= 0xdfff {
		return utf8.RuneError, next, true
	}
	return value, next, true
}

func codexExecTool(input string) (string, int, bool) {
	calls := codexExecTools(input)
	if len(calls) != 1 {
		return "", 0, false
	}
	return calls[0].name, calls[0].argumentsStart, true
}

type codexExecCall struct {
	name           string
	argumentsStart int
}

const codexExecToolNameLimit = 96

func codexExecTools(input string) []codexExecCall {
	const marker = "tools."
	var calls []codexExecCall
	for index := 0; index < len(input); {
		switch input[index] {
		case '\'', '"', '`':
			index = codexSkipJSQuoted(input, index)
			continue
		case '/':
			if index+1 < len(input) && input[index+1] == '/' {
				if end := strings.IndexByte(input[index+2:], '\n'); end >= 0 {
					index += end + 3
				} else {
					return calls
				}
				continue
			}
			if index+1 < len(input) && input[index+1] == '*' {
				if end := strings.Index(input[index+2:], "*/"); end >= 0 {
					index += end + 4
				} else {
					return calls
				}
				continue
			}
		}
		if !strings.HasPrefix(input[index:], marker) || codexJSIdentifierBefore(input, index) {
			index++
			continue
		}
		nameStart := index + len(marker)
		nameEnd := nameStart
		for nameEnd < len(input) && codexJSIdentifierPart(input[nameEnd]) {
			nameEnd++
		}
		if nameEnd > nameStart && nameEnd-nameStart <= codexExecToolNameLimit && nameEnd < len(input) && input[nameEnd] == '(' {
			calls = append(calls, codexExecCall{name: strings.Clone(input[nameStart:nameEnd]), argumentsStart: nameEnd + 1})
			index = nameEnd + 1
			continue
		}
		index++
	}
	return calls
}

func codexSkipJSQuoted(input string, start int) int {
	quote := input[start]
	for index := start + 1; index < len(input); index++ {
		if input[index] == '\\' {
			index++
			continue
		}
		if input[index] == quote {
			return index + 1
		}
	}
	return len(input)
}

func codexJSIdentifierPart(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '$'
}

func codexJSIdentifierBefore(input string, index int) bool {
	if index == 0 {
		return false
	}
	char, _ := utf8.DecodeLastRuneInString(input[:index])
	return char < utf8.RuneSelf && codexJSIdentifierPart(byte(char)) ||
		char >= utf8.RuneSelf && (unicode.IsLetter(char) || unicode.IsDigit(char) || unicode.IsMark(char) || char == '\u200c' || char == '\u200d')
}

func codexPrettyInput(input string) string {
	if bounded := model.BoundedDetailText(input); bounded != input {
		return input
	}
	raw := bytes.TrimSpace([]byte(input))
	if !codexJSONNestingWithin(raw, 32) {
		return input
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, raw, "", "  ") == nil {
		return formatted.String()
	}
	return input
}

func codexJSONNestingWithin(input []byte, limit int) bool {
	var state codexJSONNesting
	for _, char := range input {
		state.advance(char)
		if state.depth > limit {
			return false
		}
	}
	return true
}

type codexJSONNesting struct {
	depth    int
	inString bool
	escaped  bool
}

func (state *codexJSONNesting) advance(char byte) {
	if state.inString {
		if state.escaped {
			state.escaped = false
			return
		}
		if char == '\\' {
			state.escaped = true
		} else if char == '"' {
			state.inString = false
		}
		return
	}
	switch char {
	case '"':
		state.inString = true
	case '{', '[':
		state.depth++
	case '}', ']':
		state.depth--
	}
}

func codexStripExecPreamble(output string) string {
	body, _ := codexNormalizeExecOutput(output)
	return body
}

func codexNormalizeExecOutput(output string) (string, string) {
	header, rest, _ := strings.Cut(output, "\n")
	header = strings.TrimSpace(header)
	exitCode, processExit := codexExecPreambleExit(output)
	if header != "Script completed" && !codexExecRunningHeader(header) && !processExit {
		return output, ""
	}
	line, remaining, found := strings.Cut(rest, "\n")
	if codexExecWallTime(line) {
		if !found {
			return output, exitCode
		}
		line, remaining, _ = strings.Cut(remaining, "\n")
	} else if !processExit {
		return output, ""
	}
	line = strings.TrimSpace(line)
	if line != "Output:" && line != "Final output:" {
		return output, exitCode
	}
	return strings.TrimSpace(remaining), exitCode
}

func codexExecRunningHeader(header string) bool {
	const marker = "Script running with cell ID "
	id, ok := strings.CutPrefix(header, marker)
	return ok && id != "" && len(id) <= 256 && !strings.ContainsAny(id, " \t\r\n")
}

func codexExecWallTime(line string) bool {
	value, ok := strings.CutPrefix(strings.TrimSpace(line), "Wall time ")
	if !ok {
		return false
	}
	value, ok = strings.CutSuffix(value, " seconds")
	if !ok {
		return false
	}
	duration, err := strconv.ParseFloat(value, 64)
	return err == nil && duration >= 0
}

func codexExecPreambleExit(output string) (string, bool) {
	const marker = "Process exited with code "
	header, _, _ := strings.Cut(output, "\n")
	code, ok := strings.CutPrefix(strings.TrimSpace(header), marker)
	if !ok {
		return "", false
	}
	value, err := strconv.Atoi(code)
	if err != nil || value < 0 {
		return "", false
	}
	return code, true
}

func codexResultSummary(output string) string {
	const marker = "Process exited with code "
	if start := strings.Index(output, marker); start >= 0 {
		value := output[start+len(marker):]
		if end := strings.IndexAny(value, "\r\n "); end >= 0 {
			value = value[:end]
		}
		return "exit " + value
	}
	return strings.Join(strings.Fields(output), " ")
}
