package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/motoki317/agtlog/internal/cost"
	"github.com/motoki317/agtlog/internal/model"
)

type Parser struct {
	calculator          cost.Calculator
	defaultPricingModel string
}

func NewParser(calculator cost.Calculator, defaultPricingModel string) Parser {
	return Parser{calculator: calculator, defaultPricingModel: defaultPricingModel}
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type logRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type          string `json:"type"`
		Model         string `json:"model"`
		SessionID     string `json:"session_id"`
		CWD           string `json:"cwd"`
		Message       string `json:"message"`
		AgentPath     string `json:"agent_path"`
		AgentThreadID string `json:"agent_thread_id"`
		Kind          string `json:"kind"`
		Git           struct {
			Branch string `json:"branch"`
		} `json:"git"`
		Info struct {
			Total *tokenUsage `json:"total_token_usage"`
			Last  *tokenUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

func (p Parser) Parse(path string) (*model.Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	session := &model.Session{Agent: model.AgentCodex, Path: path}
	var currentModel string
	var lastTotal *tokenUsage
	var sumLast tokenUsage
	hasLast := false
	seenModels := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record logRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
			if session.StartedAt.IsZero() {
				session.StartedAt = timestamp
			}
			if session.UpdatedAt.IsZero() || timestamp.After(session.UpdatedAt) {
				session.UpdatedAt = timestamp
			}
		}
		if record.Type == "session_meta" {
			session.ID = record.Payload.SessionID
			session.CWD = record.Payload.CWD
			session.Project = filepath.Base(record.Payload.CWD)
			session.GitBranch = record.Payload.Git.Branch
		}
		if record.Type == "turn_context" && record.Payload.Model != "" {
			currentModel = record.Payload.Model
			if !seenModels[currentModel] {
				session.Models = append(session.Models, currentModel)
				seenModels[currentModel] = true
			}
		}
		if record.Type == "event_msg" && record.Payload.Type == "user_message" {
			if session.Title == "" {
				session.Title = record.Payload.Message
			}
			session.Messages++
		}
		if record.Type == "event_msg" && record.Payload.Type == "sub_agent_activity" && record.Payload.Kind == "started" {
			addSubagent(session, path, record.Payload.AgentPath, record.Payload.AgentThreadID, session.UpdatedAt)
		}
		if record.Type == "event_msg" && record.Payload.Type == "token_count" && record.Payload.Info.Total != nil {
			copy := *record.Payload.Info.Total
			lastTotal = &copy
		}
		if record.Type == "event_msg" && record.Payload.Type == "token_count" && record.Payload.Info.Last != nil {
			sumLast.InputTokens += record.Payload.Info.Last.InputTokens
			sumLast.CachedInputTokens += record.Payload.Info.Last.CachedInputTokens
			sumLast.OutputTokens += record.Payload.Info.Last.OutputTokens
			sumLast.ReasoningOutputTokens += record.Payload.Info.Last.ReasoningOutputTokens
			sumLast.TotalTokens += record.Payload.Info.Last.TotalTokens
			hasLast = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	selected := lastTotal
	if selected == nil && hasLast {
		selected = &sumLast
	}
	if selected != nil {
		session.Usage = []model.Usage{{
			Model:                  currentModel,
			InputTokens:            selected.InputTokens,
			OutputTokens:           selected.OutputTokens + selected.ReasoningOutputTokens,
			CacheReadTokens:        selected.CachedInputTokens,
			InputIncludesCacheRead: true,
		}}
		session.Cost = p.calculator.CalculateCodex(session.Usage[0], p.defaultPricingModel)
	}
	return session, nil
}

func addSubagent(root *model.Session, sourcePath, agentPath, threadID string, timestamp time.Time) {
	parts := strings.Split(strings.Trim(agentPath, "/"), "/")
	if len(parts) > 0 && parts[0] == "root" {
		parts = parts[1:]
	}
	parent := root
	for index, name := range parts {
		var child *model.Session
		for _, existing := range parent.Subagents {
			if existing.Title == name {
				child = existing
				break
			}
		}
		if child == nil {
			child = &model.Session{
				ID:        name,
				Agent:     model.AgentCodex,
				Path:      sourcePath + "#" + strings.Join(parts[:index+1], "/"),
				CWD:       root.CWD,
				Project:   root.Project,
				Title:     name,
				StartedAt: timestamp,
				UpdatedAt: timestamp,
			}
			parent.Subagents = append(parent.Subagents, child)
		}
		if index == len(parts)-1 && threadID != "" {
			child.ID = threadID
		}
		parent = child
	}
}
