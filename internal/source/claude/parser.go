package claude

import (
	"bytes"
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
	Type        string   `json:"type"`
	AITitle     string   `json:"aiTitle"`
	Timestamp   string   `json:"timestamp"`
	SessionID   string   `json:"sessionId"`
	AgentID     string   `json:"agentId"`
	CWD         string   `json:"cwd"`
	GitBranch   string   `json:"gitBranch"`
	RequestID   string   `json:"requestId"`
	IsSidechain bool     `json:"isSidechain"`
	Speed       string   `json:"speed"`
	CostUSD     *float64 `json:"costUSD"`
	Message     struct {
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
	return "claude-parser-v3:" + p.calculator.Fingerprint()
}

func (p Parser) Parse(path string) (*model.Session, error) {
	return p.parse(path, 0, make(map[string]bool))
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
		if envelope.Type != "user" && envelope.Type != "ai-title" &&
			!(envelope.Type == "assistant" && bytes.Contains(line, []byte(`"usage"`))) {
			return
		}
		var record logRecord
		if json.Unmarshal(line, &record) != nil {
			return
		}
		if record.Type == "ai-title" {
			session.Title = record.AITitle
		} else if record.Type == "user" && session.Title == "" {
			var content userContentRecord
			if json.Unmarshal(line, &content) == nil {
				session.Title = userText(content.Message.Content)
			}
		}
		if record.Type == "user" {
			userMessages++
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
		if record.Type == "assistant" && record.Message.Model != "" && record.Message.Model != "<synthetic>" {
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
		return text
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
			return block.Text
		}
	}
	return ""
}
