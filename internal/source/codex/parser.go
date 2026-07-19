package codex

import (
	"encoding/json"
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
	calculator          cost.Calculator
	defaultPricingModel string
}

func NewParser(calculator cost.Calculator, defaultPricingModel string) Parser {
	return Parser{calculator: calculator, defaultPricingModel: defaultPricingModel}
}

func (p Parser) CacheFingerprint() string {
	return "codex-parser-v3:" + p.defaultPricingModel + ":" + p.calculator.Fingerprint()
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

type userMessageRecord struct {
	Payload struct {
		Message string `json:"message"`
	} `json:"payload"`
}

func (p Parser) Parse(path string) (*model.Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	session := &model.Session{Agent: model.AgentCodex, Path: path, Cost: model.Cost{Estimated: true}}
	var currentModel string
	var lastTotal *tokenUsage
	var lastTotalModel string
	var summedLast tokenUsage
	usageByModel := make(map[string]*tokenUsage)
	var usageOrder []string
	hasLast := false
	seenModels := make(map[string]bool)
	err = jsonl.ForEach(file, func(line []byte) {
		var envelope struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type string `json:"type"`
			} `json:"payload"`
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
		if envelope.Type != "session_meta" && envelope.Type != "turn_context" &&
			!(envelope.Type == "event_msg" && (envelope.Payload.Type == "user_message" || envelope.Payload.Type == "sub_agent_activity" || envelope.Payload.Type == "token_count")) {
			return
		}
		var record logRecord
		if json.Unmarshal(line, &record) != nil {
			return
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
				var user userMessageRecord
				if json.Unmarshal(line, &user) == nil {
					session.Title = user.Payload.Message
				}
			}
			session.Messages++
		}
		if record.Type == "event_msg" && record.Payload.Type == "sub_agent_activity" && record.Payload.Kind == "started" {
			addSubagent(session, path, record.Payload.AgentPath, record.Payload.AgentThreadID, session.UpdatedAt)
		}
		if record.Type == "event_msg" && record.Payload.Type == "token_count" && record.Payload.Info.Total != nil {
			if validTokenUsage(record.Payload.Info.Total) {
				copy := *record.Payload.Info.Total
				lastTotal = &copy
				lastTotalModel = currentModel
			}
		}
		if record.Type == "event_msg" && record.Payload.Type == "token_count" && record.Payload.Info.Last != nil {
			if !validTokenUsage(record.Payload.Info.Last) {
				return
			}
			usage := usageByModel[currentModel]
			if usage == nil {
				usage = &tokenUsage{}
			}
			modelTotal := *usage
			allModelsTotal := summedLast
			if !addTokenUsage(&modelTotal, record.Payload.Info.Last) || !addTokenUsage(&allModelsTotal, record.Payload.Info.Last) {
				return
			}
			if usageByModel[currentModel] == nil {
				usageOrder = append(usageOrder, currentModel)
			}
			usageByModel[currentModel] = &modelTotal
			summedLast = allModelsTotal
			hasLast = true
		}
	})
	if err != nil {
		return nil, err
	}
	if lastTotal != nil && (!hasLast || summedLast != *lastTotal) {
		usageByModel = map[string]*tokenUsage{lastTotalModel: lastTotal}
		usageOrder = []string{lastTotalModel}
	}
	missingPricing := make(map[string]bool)
	for _, usageModel := range usageOrder {
		selected := usageByModel[usageModel]
		if selected.ReasoningOutputTokens > math.MaxInt64-selected.OutputTokens {
			continue
		}
		usage := model.Usage{
			Model:                  usageModel,
			InputTokens:            selected.InputTokens,
			OutputTokens:           selected.OutputTokens + selected.ReasoningOutputTokens,
			CacheReadTokens:        selected.CachedInputTokens,
			InputIncludesCacheRead: true,
		}
		session.Usage = append(session.Usage, usage)
		calculated := p.calculator.CalculateCodex(usage, p.defaultPricingModel)
		session.Cost.USD += calculated.USD
		session.Cost.Estimated = true
		for _, name := range calculated.MissingPricingModels {
			if !missingPricing[name] {
				session.Cost.MissingPricingModels = append(session.Cost.MissingPricingModels, name)
				missingPricing[name] = true
			}
		}
	}
	return session, nil
}

func validTokenUsage(usage *tokenUsage) bool {
	return usage.InputTokens >= 0 && usage.CachedInputTokens >= 0 && usage.OutputTokens >= 0 &&
		usage.ReasoningOutputTokens >= 0 && usage.TotalTokens >= 0 && usage.CachedInputTokens <= usage.InputTokens
}

func addTokenUsage(total *tokenUsage, delta *tokenUsage) bool {
	values := [][2]*int64{
		{&total.InputTokens, &delta.InputTokens},
		{&total.CachedInputTokens, &delta.CachedInputTokens},
		{&total.OutputTokens, &delta.OutputTokens},
		{&total.ReasoningOutputTokens, &delta.ReasoningOutputTokens},
		{&total.TotalTokens, &delta.TotalTokens},
	}
	for _, pair := range values {
		if *pair[1] > math.MaxInt64-*pair[0] {
			return false
		}
	}
	for _, pair := range values {
		*pair[0] += *pair[1]
	}
	return true
}

const (
	maxAgentPathBytes = 4 * 1024
	maxAgentDepth     = 64
	maxAgentComponent = 255
)

func addSubagent(root *model.Session, sourcePath, agentPath, threadID string, timestamp time.Time) {
	if len(agentPath) > maxAgentPathBytes {
		return
	}
	parts := strings.Split(strings.Trim(agentPath, "/"), "/")
	if len(parts) > 0 && parts[0] == "root" {
		parts = parts[1:]
	}
	if len(parts) == 0 || len(parts) > maxAgentDepth {
		return
	}
	for _, part := range parts {
		if part == "" || len(part) > maxAgentComponent {
			return
		}
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
