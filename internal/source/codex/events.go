package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

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

	calls := make(map[string]int)
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
		if record.Type == "event_msg" {
			switch record.Payload.Type {
			case "user_message":
				message := codexTimelineUserMessage(record.Payload.Message)
				if message != "" {
					appendCodexMessage(session, model.Event{Timestamp: timestamp, Kind: model.EventUser, Text: message, Model: currentModel}, false)
				}
			case "agent_message":
				if record.Payload.Message != "" {
					appendCodexMessage(session, model.Event{Timestamp: timestamp, Kind: model.EventAssistantText, Text: record.Payload.Message, Model: currentModel}, false)
				}
			case "sub_agent_activity":
				appendCodexSubagentEvent(session, record.Payload.AgentPath, record.Payload.AgentID, record.Payload.Kind, timestamp)
			case "context_compacted":
				session.Events = append(session.Events, model.Event{Timestamp: timestamp, Kind: model.EventCompact, Text: "context compacted", Model: currentModel})
			}
			return
		}
		if record.Type != "response_item" {
			return
		}
		event := model.Event{Timestamp: timestamp, Model: currentModel}
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
			event.ToolInput = codexToolInput(record.Payload.Name, input)
			event.Detail = codexToolDetail(record.Payload.Name, input)
			calls[event.CallID] = len(session.Events)
		case "function_call_output", "custom_tool_call_output":
			event.Kind, event.CallID = model.EventToolResult, record.Payload.CallID
			output := codexOutputText(record.Payload.Output)
			event.Text = model.BoundedDetailText(codexResultSummary(output))
			if index, ok := calls[event.CallID]; ok {
				call := &session.Events[index]
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
		event.ToolInput = model.BoundedDetailText(event.ToolInput)
		if event.Kind == model.EventUser || event.Kind == model.EventAssistantText {
			if event.Text != "" {
				appendCodexMessage(session, event, true)
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

func appendCodexMessage(session *model.Session, event model.Event, preferred bool) {
	event.Text = model.BoundedDetailText(model.CleanTimelineText(event.Text))
	if event.Text == "" {
		return
	}
	// A 16-event window is the deduplication ceiling for mirrored message copies.
	for index := len(session.Events) - 1; index >= 0 && index >= len(session.Events)-16; index-- {
		existing := session.Events[index]
		if existing.Kind != event.Kind || existing.Text != event.Text {
			continue
		}
		if preferred {
			session.Events[index] = event
		}
		return
	}
	session.Events = append(session.Events, event)
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
	depth := 0
	inString := false
	escaped := false
	for _, char := range input {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > limit {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
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
