package model

import "time"

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
		InputTokens:            u.InputTokens + other.InputTokens,
		OutputTokens:           u.OutputTokens + other.OutputTokens,
		CacheCreation5mTokens:  u.CacheCreation5mTokens + other.CacheCreation5mTokens,
		CacheCreation1hTokens:  u.CacheCreation1hTokens + other.CacheCreation1hTokens,
		CacheReadTokens:        u.CacheReadTokens + other.CacheReadTokens,
		InputIncludesCacheRead: u.InputIncludesCacheRead || other.InputIncludesCacheRead,
	}
}

func (u Usage) TotalTokens() int64 {
	total := u.InputTokens + u.OutputTokens + u.CacheCreation5mTokens + u.CacheCreation1hTokens
	if !u.InputIncludesCacheRead {
		total += u.CacheReadTokens
	}
	return total
}

type Cost struct {
	USD                  float64
	Estimated            bool
	MissingPricingModels []string
}

type Event struct {
	Timestamp time.Time
	Kind      string
	Text      string
	AgentID   string
}

type Session struct {
	ID        string
	Agent     AgentKind
	Path      string
	CWD       string
	Project   string
	Title     string
	Models    []string
	StartedAt time.Time
	UpdatedAt time.Time
	GitBranch string
	Messages  int
	Usage     []Usage
	Cost      Cost
	Events    []Event
	Subagents []*Session
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
