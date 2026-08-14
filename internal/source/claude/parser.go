package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
	"github.com/motoki317/agtlog/internal/source/jsonl"
)

type Parser struct {
	calculator           cost.Calculator
	walk                 func(string, filepath.WalkFunc) error
	readSubagentMetadata func(string) ([]byte, error)
}

type logRecord struct {
	Type              string          `json:"type"`
	AITitle           string          `json:"aiTitle"`
	Timestamp         string          `json:"timestamp"`
	SessionID         string          `json:"sessionId"`
	AgentID           string          `json:"agentId"`
	CWD               string          `json:"cwd"`
	GitBranch         string          `json:"gitBranch"`
	RequestID         string          `json:"requestId"`
	IsSidechain       bool            `json:"isSidechain"`
	Speed             string          `json:"speed"`
	CostUSD           *float64        `json:"costUSD"`
	ToolUseResult     json.RawMessage `json:"toolUseResult"`
	Error             json.RawMessage `json:"error"`
	APIErrorStatus    json.RawMessage `json:"apiErrorStatus"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"`
	Message           struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   claudeUsageJSON `json:"usage"`
	} `json:"message"`
}

type claudeUsageJSON struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreation            *struct {
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	} `json:"cache_creation"`
	// Iterations reports the per-step usage when one Messages request runs
	// sub-inferences server-side. The Advisor tool emits an "advisor_message"
	// step on its own model; the top-level fields deliberately exclude it because
	// it bills at that model's rates, so it must be counted separately.
	Iterations []claudeIterationJSON `json:"iterations"`
}

type claudeIterationJSON struct {
	Type  string `json:"type"`
	Model string `json:"model"`
	claudeUsageJSON
}

// claudeAdvisorUsages returns the billed usage of each Advisor sub-inference in a
// request. Anthropic excludes these tokens from the top-level usage because they
// bill at the advisor model's rates, so ignoring them undercounts every advisor
// turn. See docs/ADR/20260724-advisor-tool-cost.md.
func claudeAdvisorUsages(tokens claudeUsageJSON) []model.Usage {
	var advisor []model.Usage
	for _, iteration := range tokens.Iterations {
		if iteration.Type != "advisor_message" || iteration.Model == "" {
			continue
		}
		usage := claudeUsage(iteration.Model, iteration.claudeUsageJSON)
		if validUsage(usage) && usage.TotalTokens() > 0 {
			advisor = append(advisor, usage)
		}
	}
	return advisor
}

// claudeUsage maps a logged usage block to model.Usage. Callers that price the
// record add Speed and CostUSD; the timeline only needs the token counts.
func claudeUsage(modelName string, tokens claudeUsageJSON) model.Usage {
	usage := model.Usage{
		Model:           modelName,
		InputTokens:     tokens.InputTokens,
		OutputTokens:    tokens.OutputTokens,
		CacheReadTokens: tokens.CacheReadInputTokens,
	}
	if tokens.CacheCreation != nil {
		usage.CacheCreation1hTokens = tokens.CacheCreation.Ephemeral1hInputTokens
		usage.CacheCreation5mTokens = tokens.CacheCreation.Ephemeral5mInputTokens
	} else {
		usage.CacheCreation5mTokens = tokens.CacheCreationInputTokens
	}
	return usage
}

// claudeRequestUsage returns the billable usage an assistant line reports, or
// false for synthetic and empty lines that carry none.
func claudeRequestUsage(modelName string, tokens claudeUsageJSON) (model.Usage, bool) {
	if modelName == "" || modelName == "<synthetic>" {
		return model.Usage{}, false
	}
	usage := claudeUsage(modelName, tokens)
	if !validUsage(usage) || usage.TotalTokens() == 0 {
		return model.Usage{}, false
	}
	return usage, true
}

// setEventUsage records a request's usage and priced cost on the event that heads
// it, so the timeline can total a turn's flow and cost and read its latest context.
func (p Parser) setEventUsage(event *model.Event, usage model.Usage) {
	event.Usage = &usage
	event.Cost = p.calculator.Breakdown(usage)
	event.Priced = p.calculator.HasPricing(usage)
}

type userContentRecord struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func NewParser(calculator cost.Calculator) Parser {
	return Parser{
		calculator:           calculator,
		walk:                 filepath.Walk,
		readSubagentMetadata: readClaudeSubagentMetadata,
	}
}

func (p Parser) CacheFingerprint() string {
	return "claude-parser-v17:" + p.calculator.Fingerprint()
}

func (p Parser) Parse(path string) (*model.Session, error) {
	return p.parseSummary(path, nil)
}

func (p Parser) ParseWithDiagnostics(path string, report func(string, error)) (*model.Session, error) {
	return p.parseSummary(path, report)
}

func (p Parser) parseSummary(path string, report func(string, error)) (*model.Session, error) {
	titlePrompts := make(map[*model.Session]string)
	session, err := p.parse(path, 0, make(map[string]bool), report, titlePrompts)
	if err == nil {
		resolveSubagentTitleCollisions(session.Subagents, titlePrompts)
	}
	return session, err
}

func (p Parser) LoadEvents(ctx context.Context, session *model.Session) error {
	return p.loadEvents(ctx, session, 0, make(map[*model.Session]bool), true)
}

func (p Parser) LoadNodeEvents(ctx context.Context, session *model.Session) error {
	return p.loadEvents(ctx, session, 0, make(map[*model.Session]bool), false)
}

func (p Parser) loadEvents(ctx context.Context, session *model.Session, depth int, visited map[*model.Session]bool, recursive bool) error {
	if depth > maxSubagentDepth || visited[session] {
		return errors.New("subagent event cycle detected")
	}
	visited[session] = true
	defer delete(visited, session)
	if session.Group {
		session.Events = nil
		if recursive {
			for _, subagent := range session.Subagents {
				if err := p.loadEvents(ctx, subagent, depth+1, visited, true); err != nil {
					continue
				}
			}
		}
		return nil
	}

	info, err := os.Lstat(session.Path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("session path is not a regular file")
	}
	file, err := os.Open(session.Path)
	if err != nil {
		return err
	}
	defer file.Close()

	session.Events = nil
	calls := make(map[string]int)
	linkedSubagents := make(map[*model.Session]bool)
	// One API response is written across several lines (thinking, text, each
	// tool_use) that repeat its usage, and streaming re-logs the same request with
	// growing counts. Attribute each request to a single head event, keeping the
	// highest-token report, so a turn totals each billed request once and in full.
	headOf := make(map[string]int)
	// advisorCount tracks how many Advisor calls a message has already yielded a
	// row for, so the Nth advisor block maps to the Nth advisor_message iteration.
	// Advisor usage completes on a later line than the server_tool_use block that
	// opened the call, so collect the fullest set per message and attach it to the
	// rows once the pass finishes.
	advisorCount := make(map[string]int)
	advisorUsagesByMsg := make(map[string][]model.Usage)
	type advisorRef struct {
		eventIndex   int
		advisorIndex int
		messageID    string
	}
	var advisorRefs []advisorRef
	err = jsonl.ForEachContextWithOffset(ctx, file, func(line []byte, offset, length int64) {
		var record struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			Content   string `json:"content"`
			Timestamp string `json:"timestamp"`
			RequestID string `json:"requestId"`
			Message   struct {
				ID      string          `json:"id"`
				Model   string          `json:"model"`
				Content json.RawMessage `json:"content"`
				Usage   claudeUsageJSON `json:"usage"`
			} `json:"message"`
			ToolUseResult   json.RawMessage `json:"toolUseResult"`
			CompactMetadata struct {
				Trigger    string `json:"trigger"`
				PostTokens int64  `json:"postTokens"`
			} `json:"compactMetadata"`
		}
		// One decode per record on the hot path: a record carries the tool's whole
		// output, so every extra pass over the line rescans all of it.
		if jsonl.Unmarshal(line, &record) != nil {
			return
		}
		recordRef := model.RecordRef{Path: session.Path, Offset: offset, Length: length, Digest: sha256.Sum256(line)}
		timestamp, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
		if record.Type == "system" {
			if record.Subtype == "compact_boundary" {
				session.Events = append(session.Events, model.Event{
					Timestamp:         timestamp,
					Kind:              model.EventCompact,
					Text:              model.ElideEncrypted(record.Content),
					CompactTrigger:    record.CompactMetadata.Trigger,
					CompactPostTokens: record.CompactMetadata.PostTokens,
					RecordRef:         recordRef,
				})
			}
			return
		}
		if record.Type != "user" && record.Type != "assistant" {
			return
		}
		if record.Type == "user" {
			if text := userText(record.Message.Content); text != "" {
				// Markers are read in their own pass, and only here: a malformed
				// marker must not drop an otherwise readable record. Tool results
				// are user records too but yield no text, so the records that reach
				// this second pass are prompts — small, and a minority of the log.
				var markers struct {
					IsMeta           bool            `json:"isMeta"`
					IsCompactSummary bool            `json:"isCompactSummary"`
					PromptSource     string          `json:"promptSource"`
					Origin           json.RawMessage `json:"origin"`
				}
				_ = jsonl.Unmarshal(line, &markers)
				var origin *struct {
					Kind string `json:"kind"`
				}
				if jsonl.Unmarshal(markers.Origin, &origin) != nil {
					origin = nil
				}
				session.Events = append(session.Events, model.Event{
					Timestamp: timestamp,
					Kind:      model.EventUser,
					Text:      model.ElideEncrypted(text),
					Harness:   claudeUserIsHarness(markers.IsMeta, markers.IsCompactSummary, markers.PromptSource, origin, text),
					RecordRef: recordRef,
				})
			}
		}
		var blocks []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		}
		if jsonl.Unmarshal(record.Message.Content, &blocks) != nil {
			return
		}
		turnStart := len(session.Events)
		for _, block := range blocks {
			event := model.Event{Timestamp: timestamp, Model: record.Message.Model}
			switch block.Type {
			case "text":
				if record.Type != "assistant" {
					continue
				}
				event.Kind, event.Text = model.EventAssistantText, model.CleanTimelineText(block.Text)
				if event.Text == "" {
					continue
				}
			case "thinking":
				if record.Type != "assistant" {
					continue
				}
				event.Kind, event.Text = model.EventThinking, block.Thinking
			case "tool_use":
				if record.Type != "assistant" {
					continue
				}
				event.Kind, event.CallID, event.ToolName = model.EventToolCall, block.ID, block.Name
				event.ToolInput = model.ElideEncrypted(claudeToolInput(block.Name, block.Input))
				event.Detail = claudeToolDetail(block.Name, block.Input)
				if block.Name == "Agent" || block.Name == "Task" || block.Name == "Workflow" {
					event.Kind = model.EventSubagent
					if block.Name != "Workflow" {
						event.Subagent = matchClaudeSubagent(session.Subagents, block.Input, linkedSubagents)
					}
					if event.Subagent != nil {
						event.AgentID = event.Subagent.ID
						linkedSubagents[event.Subagent] = true
					}
				}
				calls[block.ID] = len(session.Events)
			case "tool_result":
				event.Kind, event.CallID = model.EventToolResult, block.ToolUseID
				event.Text = rawText(block.Content)
				if index, ok := calls[block.ToolUseID]; ok {
					call := &session.Events[index]
					if call.Detail != nil && block.ToolUseID != "" {
						call.Detail.Output = model.ElideEncrypted(claudeResultText(block.Content))
					}
					// Only a subagent result carries an agentId, and toolUseResult holds
					// the tool's whole output — scanning it for every tool result would
					// re-read most of the log.
					if call.Kind == model.EventSubagent {
						if call.ToolName == "Workflow" {
							if runID := toolResultRunID(record.ToolUseResult); runID != "" {
								if subagent := workflowGroupByID(session.Subagents, runID); subagent != nil {
									delete(linkedSubagents, call.Subagent)
									call.Subagent, call.AgentID = subagent, subagent.ID
									linkedSubagents[subagent] = true
									if title := toolResultWorkflowName(record.ToolUseResult); title != "" {
										subagent.Title = model.CleanTitle(title)
									}
								}
							}
						} else if agentID := toolResultAgentID(record.ToolUseResult); agentID != "" {
							if subagent := subagentByID(session.Subagents, agentID); subagent != nil {
								delete(linkedSubagents, call.Subagent)
								call.Subagent, call.AgentID = subagent, subagent.ID
								linkedSubagents[subagent] = true
							}
						}
					}
					event.Text = model.ElideEncrypted(claudeResultSummary(call.ToolName, event.Text, block.IsError))
					call.ResultSummary = event.Text
					if !timestamp.Before(call.Timestamp) {
						call.Duration = timestamp.Sub(call.Timestamp)
					}
				}
			case "server_tool_use":
				// The Advisor tool is the only server tool agtlog surfaces; it runs a
				// separate model whose usage rides in usage.iterations, so it earns its
				// own row and its own cost. Unlike reasoning blocks it is not re-logged,
				// but the guard keeps a stray re-log from doubling the row. Advisor
				// blocks follow the turn's reasoning, so the head-usage attribution
				// below never lands the executor usage on this event.
				if record.Type != "assistant" || block.Name != "advisor" {
					continue
				}
				if _, seen := calls[block.ID]; seen {
					continue
				}
				event.Kind, event.CallID, event.ToolName = model.EventAdvisor, block.ID, block.Name
				advisorRefs = append(advisorRefs, advisorRef{eventIndex: len(session.Events), advisorIndex: advisorCount[record.Message.ID], messageID: record.Message.ID})
				advisorCount[record.Message.ID]++
				calls[block.ID] = len(session.Events)
			default:
				continue
			}
			event.Text = model.ElideEncrypted(event.Text)
			if event.Text != "" || event.Kind == model.EventToolCall || event.Kind == model.EventToolResult || event.Kind == model.EventSubagent || event.Kind == model.EventAdvisor {
				event.RecordRef = recordRef
				session.Events = append(session.Events, event)
			}
		}
		if record.Type == "assistant" {
			if advisors := claudeAdvisorUsages(record.Message.Usage); len(advisors) > len(advisorUsagesByMsg[record.Message.ID]) {
				advisorUsagesByMsg[record.Message.ID] = advisors
			}
			if usage, ok := claudeRequestUsage(record.Message.Model, record.Message.Usage); ok {
				key := record.Message.ID + "\x00" + record.RequestID
				dedupe := record.Message.ID != ""
				if index, seen := headOf[key]; dedupe && seen {
					if usage.TotalTokens() > session.Events[index].Usage.TotalTokens() {
						p.setEventUsage(&session.Events[index], usage)
					}
				} else if len(session.Events) > turnStart {
					p.setEventUsage(&session.Events[turnStart], usage)
					if dedupe {
						headOf[key] = turnStart
					}
				}
			}
		}
	})
	if err != nil {
		return err
	}
	for _, ref := range advisorRefs {
		usages := advisorUsagesByMsg[ref.messageID]
		if ref.advisorIndex < len(usages) {
			usage := usages[ref.advisorIndex]
			session.Events[ref.eventIndex].Model = usage.Model
			p.setEventUsage(&session.Events[ref.eventIndex], usage)
		}
	}
	if recursive {
		for _, subagent := range session.Subagents {
			if err := p.loadEvents(ctx, subagent, depth+1, visited, true); err != nil {
				continue
			}
		}
	}
	return nil
}

func subagentByID(subagents []*model.Session, id string) *model.Session {
	for _, subagent := range subagents {
		if subagent.ID == id {
			return subagent
		}
	}
	return nil
}

func workflowGroupByID(subagents []*model.Session, id string) *model.Session {
	for _, subagent := range subagents {
		if subagent.Group && subagent.ID == id {
			return subagent
		}
	}
	return nil
}

func toolResultAgentID(result json.RawMessage) string {
	var fields struct {
		AgentID string `json:"agentId"`
	}
	_ = jsonl.Unmarshal(result, &fields)
	return fields.AgentID
}

func toolResultRunID(result json.RawMessage) string {
	var fields struct {
		RunID string `json:"runId"`
	}
	_ = jsonl.Unmarshal(result, &fields)
	return fields.RunID
}

func toolResultWorkflowName(result json.RawMessage) string {
	var fields struct {
		WorkflowName string `json:"workflowName"`
	}
	_ = jsonl.Unmarshal(result, &fields)
	return fields.WorkflowName
}

func matchClaudeSubagent(subagents []*model.Session, input json.RawMessage, linked map[*model.Session]bool) *model.Session {
	// Named fields rather than map[string]string: a Task input carries other keys
	// whose values are not strings, and decoding stops at the first one that does
	// not fit the target, which would lose the name that identifies the subagent.
	var fields struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		SubagentType string `json:"subagent_type"`
	}
	_ = jsonl.Unmarshal(input, &fields)
	candidates := []string{fields.Name, fields.Description, fields.SubagentType}
	for _, subagent := range subagents {
		if linked[subagent] {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(candidate, subagent.ID) || strings.EqualFold(candidate, subagent.Title) {
				return subagent
			}
		}
	}
	for _, subagent := range subagents {
		if !subagent.Group && !linked[subagent] {
			return subagent
		}
	}
	return nil
}

func claudeResultSummary(toolName, result string, isError bool) string {
	if toolName != "Bash" {
		return result
	}
	const marker = "Exit code "
	if start := strings.Index(result, marker); start >= 0 {
		value := result[start+len(marker):]
		if end := strings.IndexAny(value, " \r\n"); end >= 0 {
			value = value[:end]
		}
		return "exit " + value
	}
	if isError {
		return "error"
	}
	return "exit 0"
}

func claudeUserIsHarness(isMeta, isCompactSummary bool, promptSource string, origin *struct {
	Kind string `json:"kind"`
}, text string) bool {
	if isMeta || isCompactSummary || promptSource == "system" {
		return true
	}
	if origin != nil && origin.Kind != "human" {
		return true
	}
	for _, prefix := range []string{
		"<command-name>",
		"<command-message>",
		"<local-command-stdout>",
		"<bash-input>",
		"<bash-stdout>",
		"<bash-stderr>",
		"[Request interrupted by user",
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func claudeToolInput(name string, input json.RawMessage) string {
	var fields map[string]json.RawMessage
	if jsonl.Unmarshal(input, &fields) == nil {
		if name == "Edit" {
			var path, oldText, newText string
			_ = jsonl.Unmarshal(fields["file_path"], &path)
			_ = jsonl.Unmarshal(fields["old_string"], &oldText)
			_ = jsonl.Unmarshal(fields["new_string"], &newText)
			return fmt.Sprintf("%s · +%d −%d", path, textLineCount(newText), textLineCount(oldText))
		}
		key := ""
		switch name {
		case "Read", "Edit", "Write":
			key = "file_path"
		case "Bash":
			key = "command"
		case "Agent", "Task":
			key = "description"
		}
		if key != "" {
			var value string
			if jsonl.Unmarshal(fields[key], &value) == nil {
				return value
			}
		}
	}
	return rawText(input)
}

func claudeToolDetail(name string, input json.RawMessage) *model.ToolDetail {
	if name == "Agent" || name == "Task" {
		return nil
	}
	detail := &model.ToolDetail{}
	switch name {
	case "Edit":
		var fields struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		if jsonl.Unmarshal(input, &fields) != nil {
			detail.Input = model.ElideEncrypted(string(input))
			return detail
		}
		detail.Diff = model.ElideEncrypted(claudeReplaceBlock(fields.Old, fields.New))
	case "MultiEdit":
		var fields struct {
			Edits json.RawMessage `json:"edits"`
		}
		if jsonl.Unmarshal(input, &fields) != nil {
			detail.Input = model.ElideEncrypted(string(input))
			return detail
		}
		diff, ok := claudeMultiEditDiff(fields.Edits)
		if !ok {
			detail.Input = model.ElideEncrypted(string(input))
			return detail
		}
		detail.Diff = model.ElideEncrypted(diff)
	case "Write":
		var fields struct {
			Content string `json:"content"`
		}
		if jsonl.Unmarshal(input, &fields) != nil {
			detail.Input = model.ElideEncrypted(string(input))
			return detail
		}
		detail.Diff = model.ElideEncrypted(claudeReplaceBlock("", fields.Content))
	case "Bash":
		var fields struct {
			Command string `json:"command"`
		}
		if jsonl.Unmarshal(input, &fields) != nil {
			detail.Input = model.ElideEncrypted(string(input))
			return detail
		}
		detail.Input = model.ElideEncrypted(fields.Command)
	case "Read":
		var fields struct {
			FilePath string `json:"file_path"`
			Offset   *int   `json:"offset"`
			Limit    *int   `json:"limit"`
		}
		if jsonl.Unmarshal(input, &fields) != nil {
			detail.Input = model.ElideEncrypted(string(input))
			return detail
		}
		parts := []string{fields.FilePath}
		if fields.Offset != nil {
			parts = append(parts, fmt.Sprintf("offset %d", *fields.Offset))
		}
		if fields.Limit != nil {
			parts = append(parts, fmt.Sprintf("limit %d", *fields.Limit))
		}
		detail.Input = model.ElideEncrypted(strings.Join(parts, " · "))
	default:
		detail.Input = model.ElideEncrypted(claudePrettyInput(input))
	}
	return detail
}

func claudePrettyInput(input json.RawMessage) string {
	raw := string(input)
	if bounded := model.BoundedDetailText(raw); bounded != raw {
		return raw
	}
	input = bytes.TrimSpace(input)
	if !claudeJSONNestingWithin(input, 32) {
		return raw
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, input, "", "  ") == nil {
		return formatted.String()
	}
	return raw
}

func claudeJSONNestingWithin(input []byte, limit int) bool {
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

func claudeReplaceBlock(oldText, newText string) string {
	var output strings.Builder
	writeReplaceBlock(&output, oldText, newText)
	return output.String()
}

func claudeMultiEditDiff(edits json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(edits)) == 0 || bytes.Equal(bytes.TrimSpace(edits), []byte("null")) {
		return "", true
	}
	decoder := json.NewDecoder(bytes.NewReader(edits))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return "", false
	}
	var output strings.Builder
	index := 0
	for decoder.More() {
		var edit struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		if decoder.Decode(&edit) != nil {
			return "", false
		}
		if index > 0 {
			output.WriteString("\n\n")
		}
		writeReplaceBlock(&output, edit.Old, edit.New)
		index++
	}
	if _, err := decoder.Token(); err != nil {
		return "", false
	}
	return output.String(), true
}

func writeReplaceBlock(output *strings.Builder, oldText, newText string) {
	// Whole-block output is not a minimal diff; add a line-level LCS if noisy replacements make that ceiling limiting.
	if oldText != "" {
		writePrefixedLines(output, '-', oldText)
	}
	if newText != "" {
		if oldText != "" {
			output.WriteByte('\n')
		}
		writePrefixedLines(output, '+', newText)
	}
}

func writePrefixedLines(output *strings.Builder, prefix rune, text string) {
	output.WriteRune(prefix)
	for _, char := range text {
		output.WriteRune(char)
		if char == '\n' {
			output.WriteRune(prefix)
		}
	}
}

func textLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func rawText(value json.RawMessage) string {
	var text string
	if jsonl.Unmarshal(value, &text) == nil {
		return strings.Join(strings.Fields(text), " ")
	}
	return strings.Join(strings.Fields(string(value)), " ")
}

func claudeResultText(content json.RawMessage) string {
	raw := bytes.TrimSpace(content)
	if len(raw) == 0 {
		return ""
	}
	var output strings.Builder
	if raw[0] == '"' {
		var text string
		if jsonl.Unmarshal(raw, &text) != nil {
			return string(content)
		}
		output.WriteString(text)
		return output.String()
	}
	if raw[0] != '[' {
		return string(content)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if jsonl.Unmarshal(raw, &blocks) != nil {
		return string(content)
	}
	first := true
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		if !first {
			output.WriteByte('\n')
		}
		output.WriteString(block.Text)
		first = false
	}
	return output.String()
}

const maxSubagentDepth = 64
const maxRetainedTitlePromptBytes = 16 << 10
const maxRetainedTitlePromptLines = 128
const maxClaudeSubagentMetadataBytes = 64 << 10

type flatClaudeSubagent struct {
	session    *model.Session
	transcript string
	parentID   string
}

func (p Parser) parse(path string, depth int, visited map[string]bool, report func(string, error), titlePrompts map[*model.Session]string) (*model.Session, error) {
	if depth > maxSubagentDepth {
		return nil, fmt.Errorf("subagent nesting exceeds %d levels", maxSubagentDepth)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("session path is not a regular file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if visited[absolute] {
		return nil, errors.New("subagent cycle detected")
	}
	visited[absolute] = true
	defer delete(visited, absolute)

	session, workflowNames, titlePrompt, err := p.parseFile(path)
	if err != nil {
		return nil, err
	}
	if titlePrompt != "" {
		titlePrompts[session] = titlePrompt
	}
	subagentDir := filepath.Join(strings.TrimSuffix(path, filepath.Ext(path)), "subagents")
	if info, statErr := os.Lstat(subagentDir); statErr == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
		groups := make(map[string]*model.Session)
		children := make([]*model.Session, 0)
		flatChildren := make([]flatClaudeSubagent, 0)
		walk := p.walk
		if walk == nil {
			walk = filepath.Walk
		}
		walkErr := walk(subagentDir, func(subagentPath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				if report != nil {
					report(subagentPath, walkErr)
				}
				return nil
			}
			if info.IsDir() {
				if subagentPath != subagentDir && transcriptOwnsCompanionDir(subagentPath) {
					return filepath.SkipDir
				}
				if subagentPath != subagentDir && subagentTranscriptCandidate(subagentPath, subagentDir) {
					if _, parseErr := p.parse(subagentPath, depth+1, visited, report, titlePrompts); parseErr != nil && report != nil {
						report(subagentPath, parseErr)
					}
					return filepath.SkipDir
				}
				return nil
			}
			if !subagentTranscriptCandidate(subagentPath, subagentDir) {
				return nil
			}
			parentDir := filepath.Dir(subagentPath)
			subagent, parseErr := p.parse(subagentPath, depth+1, visited, report, titlePrompts)
			if parseErr != nil {
				if report != nil {
					report(subagentPath, parseErr)
				}
				return nil
			}
			if parentDir == subagentDir {
				children = append(children, subagent)
				metadataPath := strings.TrimSuffix(subagentPath, filepath.Ext(subagentPath)) + ".meta.json"
				parentID, metadataErr := p.readClaudeSubagentParentID(metadataPath)
				if metadataErr != nil {
					parentID = ""
				}
				flatChildren = append(flatChildren, flatClaudeSubagent{
					session:    subagent,
					transcript: subagentPath,
					parentID:   parentID,
				})
				return nil
			}
			groupID := filepath.Base(parentDir)
			group := groups[parentDir]
			if group == nil {
				groupTitle := workflowNames[groupID]
				if groupTitle == "" {
					groupTitle = groupID
				}
				group = &model.Session{ID: groupID, Agent: session.Agent, Path: path + "#" + groupID, Title: groupTitle, Group: true}
				groups[parentDir] = group
				children = append(children, group)
			}
			attachSubagent(group, subagent)
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
		nested := reparentFlatClaudeSubagents(flatChildren, depth+1)
		for _, child := range children {
			if !nested[child] {
				attachSubagent(session, child)
			}
		}
	}
	if depth == 0 {
		legacyPaths, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "agent-*.jsonl"))
		for _, legacyPath := range legacyPaths {
			if sessionIDFromFile(legacyPath) != session.ID {
				continue
			}
			subagent, parseErr := p.parse(legacyPath, depth+1, visited, report, titlePrompts)
			if parseErr == nil {
				attachSubagent(session, subagent)
			} else if report != nil {
				report(legacyPath, parseErr)
			}
		}
	}
	return session, nil
}

func (p Parser) readClaudeSubagentParentID(path string) (string, error) {
	readMetadata := p.readSubagentMetadata
	if readMetadata == nil {
		readMetadata = readClaudeSubagentMetadata
	}
	content, err := readMetadata(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var metadata struct {
		ParentAgentID string `json:"parentAgentId"`
	}
	if err := json.Unmarshal(content, &metadata); err != nil {
		return "", err
	}
	return metadata.ParentAgentID, nil
}

func readClaudeSubagentMetadata(path string) ([]byte, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, errors.New("subagent metadata is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return nil, errors.New("subagent metadata changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxClaudeSubagentMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxClaudeSubagentMetadataBytes {
		return nil, fmt.Errorf("subagent metadata exceeds %d bytes", maxClaudeSubagentMetadataBytes)
	}
	return content, nil
}

func reparentFlatClaudeSubagents(children []flatClaudeSubagent, rootDepth int) map[*model.Session]bool {
	byID := make(map[string]*model.Session, len(children))
	ambiguousIDs := make(map[string]bool)
	for _, child := range children {
		id := child.session.ID
		if id == "" {
			name := strings.TrimSuffix(filepath.Base(child.transcript), filepath.Ext(child.transcript))
			if strings.HasPrefix(name, "agent-") {
				id = strings.TrimPrefix(name, "agent-")
			}
		}
		if id != "" {
			if _, exists := byID[id]; exists {
				delete(byID, id)
				ambiguousIDs[id] = true
			} else if !ambiguousIDs[id] {
				byID[id] = child.session
			}
		}
	}

	requestedParents := make(map[*model.Session]*model.Session)
	for _, child := range children {
		if child.parentID == "" {
			continue
		}
		if ambiguousIDs[child.parentID] {
			continue
		}
		parent := byID[child.parentID]
		if parent == nil {
			continue
		}
		requestedParents[child.session] = parent
	}

	invalid := make(map[*model.Session]bool)
	for child, parent := range requestedParents {
		if flatSubagentParentCycle(child, parent, requestedParents) {
			invalid[child] = true
		}
	}
	rejectOverdeepFlatSubagents(children, rootDepth, requestedParents, invalid)

	nested := make(map[*model.Session]bool)
	childrenByParent := make(map[*model.Session][]*model.Session)
	for _, child := range children {
		parent := requestedParents[child.session]
		if parent == nil || invalid[child.session] {
			continue
		}
		childrenByParent[parent] = append(childrenByParent[parent], child.session)
		nested[child.session] = true
	}
	var attachDescendants func(*model.Session)
	attachDescendants = func(parent *model.Session) {
		for _, child := range childrenByParent[parent] {
			attachDescendants(child)
			attachSubagent(parent, child)
		}
	}
	for _, child := range children {
		if !nested[child.session] {
			attachDescendants(child.session)
		}
	}
	return nested
}

func rejectOverdeepFlatSubagents(
	children []flatClaudeSubagent,
	rootDepth int,
	requestedParents map[*model.Session]*model.Session,
	invalid map[*model.Session]bool,
) {
	depths := make(map[*model.Session]int, len(children))
	for _, child := range children {
		if _, known := depths[child.session]; known {
			continue
		}
		var path []*model.Session
		cursor := child.session
		baseDepth := rootDepth
		for {
			if depth, known := depths[cursor]; known {
				baseDepth = depth
				break
			}
			if invalid[cursor] || requestedParents[cursor] == nil {
				depths[cursor] = rootDepth
				break
			}
			path = append(path, cursor)
			cursor = requestedParents[cursor]
		}
		for index := len(path) - 1; index >= 0; index-- {
			node := path[index]
			if subagentTreeHeight(node) > maxSubagentDepth-baseDepth {
				invalid[node] = true
				depths[node] = rootDepth
				baseDepth = rootDepth
				continue
			}
			baseDepth++
			depths[node] = baseDepth
		}
	}
}

func subagentTreeHeight(session *model.Session) int {
	height := 1
	for _, child := range session.Subagents {
		height = max(height, 1+subagentTreeHeight(child))
	}
	return height
}

func flatSubagentParentCycle(child, parent *model.Session, requestedParents map[*model.Session]*model.Session) bool {
	seen := make(map[*model.Session]bool)
	for ancestor := parent; ancestor != nil; ancestor = requestedParents[ancestor] {
		if ancestor == child {
			return true
		}
		if seen[ancestor] {
			return false
		}
		seen[ancestor] = true
	}
	return false
}

func resolveSubagentTitleCollisions(siblings []*model.Session, titlePrompts map[*model.Session]string) {
	for _, sibling := range siblings {
		resolveSubagentTitleCollisions(sibling.Subagents, titlePrompts)
	}
	resolveSiblingTitleCollisions(siblings, titlePrompts)
}

func resolveSiblingTitleCollisions(siblings []*model.Session, titlePrompts map[*model.Session]string) {
	byTitle := make(map[string][]*model.Session)
	hasCollision := false
	for _, sibling := range siblings {
		byTitle[sibling.Title] = append(byTitle[sibling.Title], sibling)
		if len(byTitle[sibling.Title]) == 2 {
			hasCollision = true
		}
	}
	if !hasCollision {
		return
	}
	lineCounts := make(map[string]int)
	for _, sibling := range siblings {
		seen := make(map[string]bool)
		if prompt := titlePrompts[sibling]; prompt != "" {
			for len(prompt) > 0 {
				line, rest, found := strings.Cut(prompt, "\n")
				if !found {
					rest = ""
				}
				if title := model.CleanTitle(line); title != "" {
					seen[title] = true
				}
				prompt = rest
			}
		} else if sibling.Title != "" {
			seen[sibling.Title] = true
		}
		for title := range seen {
			lineCounts[title]++
		}
	}
	shared := make(map[string]bool)
	for title, count := range lineCounts {
		if count > 1 {
			shared[title] = true
		}
	}
	for title, collisions := range byTitle {
		if len(collisions) < 2 {
			continue
		}
		shared[title] = true
		for _, sibling := range collisions {
			if prompt := titlePrompts[sibling]; prompt != "" {
				sibling.Title = model.CleanUniqueTitle(prompt, shared)
			}
		}
	}
}

func subagentTranscriptCandidate(path, root string) bool {
	if filepath.Dir(path) == root {
		return filepath.Ext(path) == ".jsonl"
	}
	base := filepath.Base(path)
	return strings.HasPrefix(base, "agent-") && filepath.Ext(base) == ".jsonl"
}

func transcriptOwnsCompanionDir(path string) bool {
	info, err := os.Lstat(path + ".jsonl")
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

func attachSubagent(parent, subagent *model.Session) {
	parent.Subagents = append(parent.Subagents, subagent)
	if parent.Group {
		seenModels := make(map[string]bool, len(parent.Models))
		for _, name := range parent.Models {
			seenModels[name] = true
		}
		for _, name := range subagent.Models {
			if !seenModels[name] {
				parent.Models = append(parent.Models, name)
				seenModels[name] = true
			}
		}
		for name, childCost := range subagent.ModelCosts {
			if parent.ModelCosts == nil {
				parent.ModelCosts = make(map[string]float64)
			}
			parent.ModelCosts[name] += childCost
		}
	}
	if parent.Group && !subagent.StartedAt.IsZero() && (parent.StartedAt.IsZero() || subagent.StartedAt.Before(parent.StartedAt)) {
		parent.StartedAt = subagent.StartedAt
	}
	if subagent.UpdatedAt.After(parent.UpdatedAt) {
		parent.UpdatedAt = subagent.UpdatedAt
	}
}

func sessionIDFromFile(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	var sessionID string
	_ = jsonl.ForEach(file, func(line []byte) {
		if sessionID != "" {
			return
		}
		var envelope struct {
			SessionID string `json:"sessionId"`
		}
		if jsonl.Unmarshal(line, &envelope) == nil {
			sessionID = envelope.SessionID
		}
	})
	return sessionID
}

func (p Parser) parseFile(path string) (*model.Session, map[string]string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, "", err
	}
	defer file.Close()

	session := &model.Session{Agent: model.AgentClaude, Path: path}
	workflowNames := make(map[string]string)
	var titlePrompt string
	hasAITitle := false
	var usageRecords []usageRecord
	// messages counts this session's own conversation turns: user prompts (records
	// carrying text, not tool-result-only) plus assistant text replies. It matches
	// the user+assistant message lines the detail timeline shows, so a session with
	// many interactions no longer reads as one message.
	messages := 0
	err = jsonl.ForEach(file, func(line []byte) {
		var envelope struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
		}
		if jsonl.Unmarshal(line, &envelope) != nil {
			return
		}
		if timestamp, parseErr := time.Parse(time.RFC3339Nano, envelope.Timestamp); parseErr == nil {
			if session.StartedAt.IsZero() {
				session.StartedAt = timestamp
			}
			if session.UpdatedAt.IsZero() || timestamp.After(session.UpdatedAt) {
				session.UpdatedAt = timestamp
			}
		}
		hasUsage := bytes.Contains(line, []byte(`"usage"`))
		if envelope.Type != "user" && envelope.Type != "ai-title" && envelope.Type != "assistant" {
			return
		}
		var record logRecord
		if jsonl.Unmarshal(line, &record) != nil {
			return
		}
		if runID := toolResultRunID(record.ToolUseResult); runID != "" {
			if title := toolResultWorkflowName(record.ToolUseResult); title != "" {
				workflowNames[runID] = model.CleanTitle(title)
			}
		}
		switch record.Type {
		case "ai-title":
			if title := model.CleanTitle(record.AITitle); title != "" {
				session.Title = title
				hasAITitle = true
				titlePrompt = ""
			}
		case "user":
			var content userContentRecord
			if jsonl.Unmarshal(line, &content) == nil {
				if text := userText(content.Message.Content); text != "" {
					messages++
					if session.Title == "" && !hasAITitle {
						session.Title = model.CleanTitle(text)
						if session.Title != "" {
							titlePrompt = retainedTitlePrompt(text)
						}
					}
				}
			}
		case "assistant":
			messages += assistantTextBlocks(record.Message.Content)
		}
		if record.Type == "assistant" && (record.IsAPIErrorMessage || meaningfulJSON(record.Error) || meaningfulJSON(record.APIErrorStatus)) {
			session.HasError = true
		}
		if session.ID == "" {
			if record.AgentID != "" {
				session.ID = record.AgentID
			} else {
				session.ID = record.SessionID
			}
		}
		if session.CWD == "" && record.CWD != "" {
			session.CWD = record.CWD
			session.Project = filepath.Base(record.CWD)
		}
		if session.GitBranch == "" {
			session.GitBranch = record.GitBranch
		}
		if record.Type == "assistant" && hasUsage && record.Message.Model != "" && record.Message.Model != "<synthetic>" {
			usage := claudeUsage(record.Message.Model, record.Message.Usage)
			usage.Speed = record.Speed
			usage.CostUSD = record.CostUSD
			if !validUsage(usage) {
				return
			}
			usageRecords = append(usageRecords, usageRecord{
				MessageID:   record.Message.ID,
				RequestID:   record.RequestID,
				IsSidechain: record.IsSidechain,
				Usage:       usage,
			})
			for index, advisor := range claudeAdvisorUsages(record.Message.Usage) {
				usageRecords = append(usageRecords, usageRecord{
					MessageID:   fmt.Sprintf("%s\x00advisor\x00%d", record.Message.ID, index),
					RequestID:   record.RequestID,
					IsSidechain: record.IsSidechain,
					Usage:       advisor,
				})
			}
		}
	})
	if err != nil {
		return nil, nil, "", err
	}
	seenModels := make(map[string]bool)
	missingPricing := make(map[string]bool)
	for _, record := range deduplicate(usageRecords) {
		session.Usage = append(session.Usage, record.Usage)
		calculated := p.calculator.Calculate(record.Usage)
		session.Requests = append(session.Requests, model.RequestUsage{
			MessageID: record.MessageID,
			RequestID: record.RequestID,
			Usage:     record.Usage,
			USD:       calculated.USD,
		})
		if !seenModels[record.Usage.Model] {
			session.Models = append(session.Models, record.Usage.Model)
			seenModels[record.Usage.Model] = true
		}
		if session.ModelCosts == nil {
			session.ModelCosts = make(map[string]float64)
		}
		session.ModelCosts[record.Usage.Model] += calculated.USD
		if p.calculator.HasPricing(record.Usage) {
			if session.ModelCostBreakdowns == nil {
				session.ModelCostBreakdowns = make(map[string]model.CostBreakdown)
			}
			current := session.ModelCostBreakdowns[record.Usage.Model]
			session.ModelCostBreakdowns[record.Usage.Model] = current.Add(p.calculator.Breakdown(record.Usage))
		}
		session.Cost.USD += calculated.USD
		session.Cost.Estimated = session.Cost.Estimated || calculated.Estimated
		for _, name := range calculated.MissingPricingModels {
			if !missingPricing[name] {
				session.Cost.MissingPricingModels = append(session.Cost.MissingPricingModels, name)
				missingPricing[name] = true
			}
		}
	}
	session.Messages = messages
	return session, workflowNames, titlePrompt, nil
}

func retainedTitlePrompt(prompt string) string {
	if len(prompt) > maxRetainedTitlePromptBytes || strings.Count(prompt, "\n") >= maxRetainedTitlePromptLines {
		return ""
	}
	return prompt
}

// assistantTextBlocks counts the text blocks in an assistant message that the
// detail timeline renders as their own message lines, applying the same
// non-empty predicate so the list count and the timeline agree.
func assistantTextBlocks(content json.RawMessage) int {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if jsonl.Unmarshal(content, &blocks) != nil {
		return 0
	}
	count := 0
	for _, block := range blocks {
		if block.Type == "text" && model.CleanTimelineText(block.Text) != "" {
			count++
		}
	}
	return count
}

func meaningfulJSON(value json.RawMessage) bool {
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return false
	}
	switch string(value) {
	case "null", "false", "0", `""`:
		return false
	default:
		return true
	}
}

func validUsage(usage model.Usage) bool {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheCreation5mTokens < 0 ||
		usage.CacheCreation1hTokens < 0 || usage.CacheReadTokens < 0 {
		return false
	}
	return usage.CostUSD == nil || (*usage.CostUSD >= 0 && !math.IsInf(*usage.CostUSD, 0) && !math.IsNaN(*usage.CostUSD))
}

func userText(content json.RawMessage) string {
	var text string
	if jsonl.Unmarshal(content, &text) == nil {
		return model.CleanTimelineText(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if jsonl.Unmarshal(content, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			if text := model.CleanTimelineText(block.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
