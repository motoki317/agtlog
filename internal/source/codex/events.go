package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

func (p Parser) loadEvents(ctx context.Context, session *model.Session) error {
	clearCodexEvents(session)
	return p.loadEventsRecursive(ctx, session, 0, make(map[string]bool))
}

func (p Parser) loadEventsRecursive(ctx context.Context, session *model.Session, depth int, visited map[string]bool) error {
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
	}
	calls := make(map[string]pendingCall)
	dedupTextByEvent := make(map[int]string)
	currentModel := ""
	isSubagent := session.AgentPath != ""
	active := !isSubagent
	err = jsonl.ForEachContext(ctx, file, func(line []byte) {
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
				Summary      []codexTextBlock `json:"summary"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			return
		}
		timestamp, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
		if record.Type == "session_meta" {
			if record.Payload.ThreadSource == "subagent" {
				isSubagent, active = true, false
			}
			return
		}
		if record.Type == "inter_agent_communication_metadata" {
			if isSubagent {
				active = true
			}
			return
		}
		if !active {
			return
		}
		if record.Type == "turn_context" {
			currentModel = record.Payload.Model
			return
		}
		event := model.Event{Timestamp: timestamp, Model: currentModel}
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
				appendCodexSubagentEvent(session, record.Payload.AgentPath, record.Payload.AgentID, record.Payload.Kind, timestamp)
				return
			case "context_compacted":
				session.Events = append(session.Events, model.Event{Timestamp: timestamp, Kind: model.EventCompact, Text: "context compacted", Model: currentModel})
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
					event.Text = codexTimelineUserMessage(event.Text)
				}
				if event.Text == "" {
					return
				}
			case "reasoning":
				event.Kind, event.Text = model.EventThinking, joinCodexText(record.Payload.Summary)
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
				event.Text = model.BoundedDetailText(summary)
				if linked {
					call := &session.Events[pending.eventIndex]
					if call.Detail != nil && event.CallID != "" {
						call.Detail.Output = model.BoundedDetailText(output)
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
		event.ToolInput = model.BoundedDetailText(event.ToolInput)
		if event.Kind == model.EventUser || event.Kind == model.EventAssistantText {
			if event.Text != "" {
				appendCodexMessage(session, event, preferredMessage, dedupText, dedupTextByEvent)
			}
		} else {
			event.Text = model.BoundedDetailText(event.Text)
			if event.Text != "" || event.Kind == model.EventToolCall || event.Kind == model.EventToolResult {
				session.Events = append(session.Events, event)
			}
		}
	})
	if err != nil {
		return err
	}
	for _, subagent := range session.Subagents {
		if subagent.Path == "" || strings.Contains(subagent.Path, "#") {
			continue
		}
		if err := p.loadEventsRecursive(ctx, subagent, depth+1, visited); err != nil {
			return err
		}
	}
	return nil
}

func appendCodexMessage(session *model.Session, event model.Event, preferred bool, dedupText string, dedupTextByEvent map[int]string) {
	event.Text = model.BoundedDetailText(model.CleanTimelineText(event.Text))
	dedupText = model.BoundedDetailText(model.CleanTimelineText(dedupText))
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
		existingText := existing.Text
		if text, ok := dedupTextByEvent[index]; ok {
			existingText = text
		}
		if existing.Kind != event.Kind || existingText != dedupText {
			continue
		}
		if preferred {
			session.Events[index] = event
			if dedupTextByEvent != nil {
				dedupTextByEvent[index] = dedupText
			}
		}
		return
	}
	index := len(session.Events)
	session.Events = append(session.Events, event)
	if dedupTextByEvent != nil {
		dedupTextByEvent[index] = dedupText
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

func appendCodexSubagentEvent(root *model.Session, agentPath, agentID, activity string, timestamp time.Time) {
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
				parent.Events = append(parent.Events, model.Event{Timestamp: timestamp, Kind: model.EventSubagent, AgentID: agentID, Subagent: child})
			} else {
				child.Events = append(child.Events, model.Event{Timestamp: timestamp, Kind: model.EventSystem, Text: activity, AgentID: agentID})
			}
			return
		}
		parent = child
	}
}

func joinCodexText(blocks []codexTextBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block.Text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func codexElideEncrypted(text string) string {
	const (
		prefix    = "gAAAA"
		minLength = 64
	)
	var elided strings.Builder
	searchFrom, writeFrom := 0, 0
	for searchFrom < len(text) {
		offset := strings.Index(text[searchFrom:], prefix)
		if offset < 0 {
			break
		}
		start := searchFrom + offset
		if start > 0 && codexFernetChar(text[start-1]) {
			searchFrom = start + len(prefix)
			continue
		}
		end := start + len(prefix)
		for end < len(text) && codexFernetChar(text[end]) {
			end++
		}
		for end < len(text) && text[end] == '=' {
			end++
		}
		if end-start < minLength {
			searchFrom = start + len(prefix)
			continue
		}
		if elided.Len() == 0 {
			elided.Grow(len(text))
		}
		elided.WriteString(text[writeFrom:start])
		fmt.Fprintf(&elided, "<encrypted %d chars>", end-start)
		writeFrom, searchFrom = end, end
	}
	if elided.Len() == 0 {
		return text
	}
	elided.WriteString(text[writeFrom:])
	return elided.String()
}

func codexFernetChar(char byte) bool {
	return char >= 'A' && char <= 'Z' ||
		char >= 'a' && char <= 'z' ||
		char >= '0' && char <= '9' ||
		char == '_' || char == '-'
}

func codexOutputText(output json.RawMessage) string {
	raw := bytes.TrimSpace(output)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return strings.TrimSpace(string(output))
		}
		return text
	}
	if raw[0] == '[' {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if _, err := decoder.Token(); err == nil {
			var text strings.Builder
			first := true
			for decoder.More() {
				var block codexTextBlock
				if decoder.Decode(&block) != nil {
					return strings.TrimSpace(string(output))
				}
				if !first {
					text.WriteByte('\n')
				}
				text.WriteString(block.Text)
				first = false
			}
			if _, err := decoder.Token(); err == nil {
				return text.String()
			}
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
	if json.Unmarshal([]byte(input), &fields) == nil {
		key := ""
		switch name {
		case "exec_command":
			key = "cmd"
		case "view_image", "Read", "Edit", "Write":
			key = "path"
		}
		if key != "" {
			var value string
			if json.Unmarshal(fields[key], &value) == nil {
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
		detail.Diff = model.BoundedDetailText(input)
	case "exec_command":
		var fields struct {
			Command string `json:"cmd"`
		}
		if json.Unmarshal([]byte(input), &fields) == nil {
			detail.Input = model.BoundedDetailText(fields.Command)
		} else {
			detail.Input = model.BoundedDetailText(input)
		}
	default:
		detail.Input = model.BoundedDetailText(codexPrettyInput(input))
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
		return "", &model.ToolDetail{Input: model.BoundedDetailText(codexPrettyInput(input))}
	}
	name, _, wrapped := codexExecTool(input)
	if wrapped && name != "exec_command" {
		return "", &model.ToolDetail{Input: model.BoundedDetailText(codexPrettyInput(input))}
	}
	command, ok := codexExecCommand(input)
	if !ok {
		return strings.Join(strings.Fields(input), " "), &model.ToolDetail{Input: model.BoundedDetailText(codexPrettyInput(input))}
	}
	return strings.SplitN(command, "\n", 2)[0], &model.ToolDetail{Input: model.BoundedDetailText(command)}
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
	var state codexJSONNesting
	for index := objectStart; index < len(input); index++ {
		state.advance(input[index])
		if input[index] == '}' && state.depth == 0 && !state.inString {
			var fields struct {
				Command *string `json:"cmd"`
				Workdir string  `json:"workdir"`
			}
			if json.Unmarshal([]byte(input[objectStart:index+1]), &fields) != nil || fields.Command == nil || *fields.Command == "" {
				return "", false
			}
			return *fields.Command, true
		}
	}
	return "", false
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
		return bounded
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
