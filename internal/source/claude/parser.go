package claude

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
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
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

func NewParser(calculator cost.Calculator) Parser {
	return Parser{calculator: calculator}
}

func (p Parser) Parse(path string) (*model.Session, error) {
	session, err := p.parseFile(path)
	if err != nil {
		return nil, err
	}
	subagentPattern := filepath.Join(strings.TrimSuffix(path, filepath.Ext(path)), "subagents", "*.jsonl")
	subagentPaths, err := filepath.Glob(subagentPattern)
	if err != nil {
		return nil, err
	}
	for _, subagentPath := range subagentPaths {
		subagent, err := p.Parse(subagentPath)
		if err != nil {
			return nil, err
		}
		session.Subagents = append(session.Subagents, subagent)
	}
	return session, nil
}

func (p Parser) parseFile(path string) (*model.Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	session := &model.Session{Agent: model.AgentClaude, Path: path}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var usageRecords []usageRecord
	userMessages := 0
	for scanner.Scan() {
		var record logRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if record.Type == "ai-title" {
			session.Title = record.AITitle
		} else if record.Type == "user" && session.Title == "" {
			session.Title = userText(record.Message.Content)
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
		if timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
			if session.StartedAt.IsZero() {
				session.StartedAt = timestamp
			}
			if session.UpdatedAt.IsZero() || timestamp.After(session.UpdatedAt) {
				session.UpdatedAt = timestamp
			}
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
			usageRecords = append(usageRecords, usageRecord{
				MessageID:   record.Message.ID,
				RequestID:   record.RequestID,
				IsSidechain: record.IsSidechain,
				Usage:       usage,
			})
		}
	}
	if err := scanner.Err(); err != nil {
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
