package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	calculator cost.Calculator
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
	Error             json.RawMessage `json:"error"`
	APIErrorStatus    json.RawMessage `json:"apiErrorStatus"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"`
	Message           struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
				Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

type userContentRecord struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func NewParser(calculator cost.Calculator) Parser {
	return Parser{calculator: calculator}
}

func (p Parser) CacheFingerprint() string {
	return "claude-parser-v8:" + p.calculator.Fingerprint()
}

func (p Parser) Parse(path string) (*model.Session, error) {
	return p.parse(path, 0, make(map[string]bool))
}

func (p Parser) LoadEvents(ctx context.Context, session *model.Session) error {
	return p.loadEvents(ctx, session, 0, make(map[*model.Session]bool))
}

func (p Parser) loadEvents(ctx context.Context, session *model.Session, depth int, visited map[*model.Session]bool) error {
	if depth > maxSubagentDepth || visited[session] {
		return errors.New("subagent event cycle detected")
	}
	visited[session] = true
	defer delete(visited, session)

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
	err = jsonl.ForEachContext(ctx, file, func(line []byte) {
		var record struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			Content   string `json:"content"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model   string          `json:"model"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"toolUseResult"`
		}
		if json.Unmarshal(line, &record) != nil {
			return
		}
		timestamp, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
		if record.Type == "system" {
			if record.Subtype == "compact_boundary" {
				session.Events = append(session.Events, model.Event{Timestamp: timestamp, Kind: model.EventCompact, Text: model.BoundedDetailText(record.Content)})
			}
			return
		}
		if record.Type != "user" && record.Type != "assistant" {
			return
		}
		if record.Type == "user" {
			if text := userText(record.Message.Content); text != "" {
				session.Events = append(session.Events, model.Event{Timestamp: timestamp, Kind: model.EventUser, Text: model.BoundedDetailText(text)})
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
		if json.Unmarshal(record.Message.Content, &blocks) != nil {
			return
		}
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
				event.ToolInput = model.BoundedDetailText(claudeToolInput(block.Name, block.Input))
				event.Detail = claudeToolDetail(block.Name, block.Input)
				if block.Name == "Agent" || block.Name == "Task" {
					event.Kind = model.EventSubagent
					event.Subagent = matchClaudeSubagent(session.Subagents, block.Input, linkedSubagents)
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
						call.Detail.Output = model.BoundedDetailText(claudeResultText(block.Content))
					}
					if agentID := toolResultAgentID(record.ToolUseResult); call.Kind == model.EventSubagent && agentID != "" {
						if subagent := subagentByID(session.Subagents, agentID); subagent != nil {
							delete(linkedSubagents, call.Subagent)
							call.Subagent, call.AgentID = subagent, subagent.ID
							linkedSubagents[subagent] = true
						}
					}
					event.Text = model.BoundedDetailText(claudeResultSummary(call.ToolName, event.Text, block.IsError))
					call.ResultSummary = event.Text
					if !timestamp.Before(call.Timestamp) {
						call.Duration = timestamp.Sub(call.Timestamp)
					}
				}
			default:
				continue
			}
			event.Text = model.BoundedDetailText(event.Text)
			if event.Text != "" || event.Kind == model.EventToolCall || event.Kind == model.EventToolResult || event.Kind == model.EventSubagent {
				session.Events = append(session.Events, event)
			}
		}
	})
	if err != nil {
		return err
	}
	for _, subagent := range session.Subagents {
		if err := p.loadEvents(ctx, subagent, depth+1, visited); err != nil {
			continue
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

func toolResultAgentID(result json.RawMessage) string {
	var fields struct {
		AgentID string `json:"agentId"`
	}
	_ = json.Unmarshal(result, &fields)
	return fields.AgentID
}

func matchClaudeSubagent(subagents []*model.Session, input json.RawMessage, linked map[*model.Session]bool) *model.Session {
	var fields map[string]string
	_ = json.Unmarshal(input, &fields)
	candidates := []string{fields["name"], fields["description"], fields["subagent_type"]}
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
		if !linked[subagent] {
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

func claudeToolInput(name string, input json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) == nil {
		if name == "Edit" {
			var path, oldText, newText string
			_ = json.Unmarshal(fields["file_path"], &path)
			_ = json.Unmarshal(fields["old_string"], &oldText)
			_ = json.Unmarshal(fields["new_string"], &newText)
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
			if json.Unmarshal(fields[key], &value) == nil {
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
		if json.Unmarshal(input, &fields) != nil {
			detail.Input = model.BoundedDetailText(string(input))
			return detail
		}
		detail.Diff = model.BoundedDetailText(claudeReplaceBlock(fields.Old, fields.New))
	case "MultiEdit":
		var fields struct {
			Edits json.RawMessage `json:"edits"`
		}
		if json.Unmarshal(input, &fields) != nil {
			detail.Input = model.BoundedDetailText(string(input))
			return detail
		}
		diff, ok := claudeMultiEditDiff(fields.Edits)
		if !ok {
			detail.Input = model.BoundedDetailText(string(input))
			return detail
		}
		detail.Diff = model.BoundedDetailText(diff)
	case "Write":
		var fields struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(input, &fields) != nil {
			detail.Input = model.BoundedDetailText(string(input))
			return detail
		}
		detail.Diff = model.BoundedDetailText(claudeReplaceBlock("", fields.Content))
	case "Bash":
		var fields struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &fields) != nil {
			detail.Input = model.BoundedDetailText(string(input))
			return detail
		}
		detail.Input = model.BoundedDetailText(fields.Command)
	case "Read":
		var fields struct {
			FilePath string `json:"file_path"`
			Offset   *int   `json:"offset"`
			Limit    *int   `json:"limit"`
		}
		if json.Unmarshal(input, &fields) != nil {
			detail.Input = model.BoundedDetailText(string(input))
			return detail
		}
		parts := []string{fields.FilePath}
		if fields.Offset != nil {
			parts = append(parts, fmt.Sprintf("offset %d", *fields.Offset))
		}
		if fields.Limit != nil {
			parts = append(parts, fmt.Sprintf("limit %d", *fields.Limit))
		}
		detail.Input = model.BoundedDetailText(strings.Join(parts, " · "))
	default:
		detail.Input = model.BoundedDetailText(claudePrettyInput(input))
	}
	return detail
}

func claudePrettyInput(input json.RawMessage) string {
	raw := string(input)
	if bounded := model.BoundedDetailText(raw); bounded != raw {
		return bounded
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
	if json.Unmarshal(value, &text) == nil {
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
		if json.Unmarshal(raw, &text) != nil {
			return string(content)
		}
		output.WriteString(text)
		return output.String()
	}
	if raw[0] != '[' {
		return string(content)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return string(content)
	}
	first := true
	for decoder.More() {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if decoder.Decode(&block) != nil {
			return string(content)
		}
		if block.Type != "text" {
			continue
		}
		if !first {
			output.WriteByte('\n')
		}
		output.WriteString(block.Text)
		first = false
	}
	if _, err := decoder.Token(); err != nil {
		return string(content)
	}
	return output.String()
}

const maxSubagentDepth = 64

func (p Parser) parse(path string, depth int, visited map[string]bool) (*model.Session, error) {
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

	session, err := p.parseFile(path)
	if err != nil {
		return nil, err
	}
	subagentDir := filepath.Join(strings.TrimSuffix(path, filepath.Ext(path)), "subagents")
	if info, statErr := os.Lstat(subagentDir); statErr == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
		subagentPattern := filepath.Join(subagentDir, "*.jsonl")
		subagentPaths, globErr := filepath.Glob(subagentPattern)
		if globErr != nil {
			return nil, globErr
		}
		for _, subagentPath := range subagentPaths {
			subagent, parseErr := p.parse(subagentPath, depth+1, visited)
			if parseErr != nil {
				continue
			}
			attachSubagent(session, subagent)
		}
	}
	if depth == 0 {
		legacyPaths, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "agent-*.jsonl"))
		for _, legacyPath := range legacyPaths {
			if sessionIDFromFile(legacyPath) != session.ID {
				continue
			}
			subagent, parseErr := p.parse(legacyPath, depth+1, visited)
			if parseErr == nil {
				attachSubagent(session, subagent)
			}
		}
	}
	return session, nil
}

func attachSubagent(parent, subagent *model.Session) {
	parent.Subagents = append(parent.Subagents, subagent)
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
		if json.Unmarshal(line, &envelope) == nil {
			sessionID = envelope.SessionID
		}
	})
	return sessionID
}

func (p Parser) parseFile(path string) (*model.Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	session := &model.Session{Agent: model.AgentClaude, Path: path}
	var usageRecords []usageRecord
	userMessages := 0
	err = jsonl.ForEach(file, func(line []byte) {
		var envelope struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &envelope) != nil {
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
		if json.Unmarshal(line, &record) != nil {
			return
		}
		if record.Type == "ai-title" {
			session.Title = model.CleanTitle(record.AITitle)
		} else if record.Type == "user" && session.Title == "" {
			var content userContentRecord
			if json.Unmarshal(line, &content) == nil {
				session.Title = model.CleanTitle(userText(content.Message.Content))
			}
		}
		if record.Type == "user" {
			userMessages++
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
			usage := model.Usage{
				Model:           record.Message.Model,
				InputTokens:     record.Message.Usage.InputTokens,
				OutputTokens:    record.Message.Usage.OutputTokens,
				CacheReadTokens: record.Message.Usage.CacheReadInputTokens,
				Speed:           record.Speed,
				CostUSD:         record.CostUSD,
			}
			if record.Message.Usage.CacheCreation != nil {
				usage.CacheCreation1hTokens = record.Message.Usage.CacheCreation.Ephemeral1hInputTokens
				usage.CacheCreation5mTokens = record.Message.Usage.CacheCreation.Ephemeral5mInputTokens
			} else {
				usage.CacheCreation5mTokens = record.Message.Usage.CacheCreationInputTokens
			}
			if !validUsage(usage) {
				return
			}
			usageRecords = append(usageRecords, usageRecord{
				MessageID:   record.Message.ID,
				RequestID:   record.RequestID,
				IsSidechain: record.IsSidechain,
				Usage:       usage,
			})
		}
	})
	if err != nil {
		return nil, err
	}
	seenModels := make(map[string]bool)
	missingPricing := make(map[string]bool)
	for _, record := range deduplicate(usageRecords) {
		session.Usage = append(session.Usage, record.Usage)
		if !seenModels[record.Usage.Model] {
			session.Models = append(session.Models, record.Usage.Model)
			seenModels[record.Usage.Model] = true
		}
		calculated := p.calculator.Calculate(record.Usage)
		if session.ModelCosts == nil {
			session.ModelCosts = make(map[string]float64)
		}
		session.ModelCosts[record.Usage.Model] += calculated.USD
		session.Cost.USD += calculated.USD
		session.Cost.Estimated = session.Cost.Estimated || calculated.Estimated
		for _, name := range calculated.MissingPricingModels {
			if !missingPricing[name] {
				session.Cost.MissingPricingModels = append(session.Cost.MissingPricingModels, name)
				missingPricing[name] = true
			}
		}
	}
	session.Messages = userMessages + len(session.Usage)
	return session, nil
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
	if json.Unmarshal(content, &text) == nil {
		return model.CleanTimelineText(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			if text := model.CleanTimelineText(block.Text); text != "" {
				return text
			}
		}
	}
	return ""
}
