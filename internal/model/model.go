package model

import (
	"math"
	"time"
)

type AgentKind string

const (
	AgentClaude AgentKind = "claude"
	AgentCodex  AgentKind = "codex"
)

type Usage struct {
	Model                  string
	InputTokens            int64
	OutputTokens           int64
	CacheCreation5mTokens  int64
	CacheCreation1hTokens  int64
	CacheReadTokens        int64
	InputIncludesCacheRead bool
	Speed                  string
	CostUSD                *float64
}

func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:            saturatingAdd(u.InputTokens, other.InputTokens),
		OutputTokens:           saturatingAdd(u.OutputTokens, other.OutputTokens),
		CacheCreation5mTokens:  saturatingAdd(u.CacheCreation5mTokens, other.CacheCreation5mTokens),
		CacheCreation1hTokens:  saturatingAdd(u.CacheCreation1hTokens, other.CacheCreation1hTokens),
		CacheReadTokens:        saturatingAdd(u.CacheReadTokens, other.CacheReadTokens),
		InputIncludesCacheRead: u.InputIncludesCacheRead || other.InputIncludesCacheRead,
	}
}

func (u Usage) TotalTokens() int64 {
	total := saturatingAdd(u.InputTokens, u.OutputTokens)
	total = saturatingAdd(total, u.CacheCreation5mTokens)
	total = saturatingAdd(total, u.CacheCreation1hTokens)
	if !u.InputIncludesCacheRead {
		total = saturatingAdd(total, u.CacheReadTokens)
	}
	return total
}

func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

type Cost struct {
	USD                  float64
	Estimated            bool
	MissingPricingModels []string
}

type EventKind string

const (
	EventUser          EventKind = "user"
	EventAssistantText EventKind = "assistant-text"
	EventThinking      EventKind = "thinking"
	EventToolCall      EventKind = "tool-call"
	EventToolResult    EventKind = "tool-result"
	EventSubagent      EventKind = "subagent"
	EventSystem        EventKind = "system"
	EventCompact       EventKind = "compact"
)

// ToolDetail is the full tool payload shown when a tool call is expanded.
// Fields are bounded (BoundedDetailText) and empty when the log lacked them.
type ToolDetail struct {
	Input  string // Full invocation; newlines preserved.
	Diff   string // Unified-diff body for edits, writes, and patches.
	Output string // Full result; newlines preserved.
}

type Event struct {
	Timestamp time.Time
	Kind      EventKind
	Text      string
	// Raw is the structurally complete source JSON after encrypted-token elision and length bounding, not a byte-verbatim copy. A verbatim view would require lazily rereading the source line on toggle.
	Raw           string
	Model         string
	CallID        string
	ToolName      string
	ToolInput     string
	ResultSummary string
	Detail        *ToolDetail
	Duration      time.Duration
	AgentID       string
	Subagent      *Session
}

type Session struct {
	ID         string
	Agent      AgentKind
	Path       string
	CWD        string
	Project    string
	Title      string
	Models     []string
	StartedAt  time.Time
	UpdatedAt  time.Time
	GitBranch  string
	AgentPath  string
	ParentID   string
	HasError   bool
	Messages   int
	Usage      []Usage
	ModelCosts map[string]float64
	Cost       Cost
	Events     []Event
	Subagents  []*Session
}

func (s Session) TotalUsage() Usage {
	var total Usage
	for _, usage := range s.Usage {
		total = total.Add(usage)
	}
	for _, subagent := range s.Subagents {
		total = total.Add(subagent.TotalUsage())
	}
	return total
}

func (s Session) TotalCost() Cost {
	total := s.Cost
	seen := make(map[string]bool, len(total.MissingPricingModels))
	for _, name := range total.MissingPricingModels {
		seen[name] = true
	}
	for _, subagent := range s.Subagents {
		subtotal := subagent.TotalCost()
		total.USD += subtotal.USD
		total.Estimated = total.Estimated || subtotal.Estimated
		for _, name := range subtotal.MissingPricingModels {
			if !seen[name] {
				total.MissingPricingModels = append(total.MissingPricingModels, name)
				seen[name] = true
			}
		}
	}
	return total
}
