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

type RequestUsage struct {
	MessageID string
	RequestID string
	Usage     Usage
	USD       float64
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

// PromptTokens is the size of the request prompt. Because the API is stateless
// the whole conversation is re-sent each request, so this equals the context
// window occupied at that request. Cache reads are part of the prompt: input
// already counts them when the source folds them in (Codex) and does not when it
// keeps them separate (Claude); cache writes are prompt tokens either way.
func (u Usage) PromptTokens() int64 {
	prompt := u.InputTokens
	if !u.InputIncludesCacheRead {
		prompt = saturatingAdd(prompt, u.CacheReadTokens)
	}
	prompt = saturatingAdd(prompt, u.CacheCreation5mTokens)
	prompt = saturatingAdd(prompt, u.CacheCreation1hTokens)
	return prompt
}

// FlowTokens is the new tokens a request adds: freshly written input plus all
// output, excluding context re-read from cache. It answers "what did this turn
// add", the per-turn counterpart to the cumulative PromptTokens.
func (u Usage) FlowTokens() int64 {
	input := u.InputTokens
	if u.InputIncludesCacheRead {
		input = max(0, input-u.CacheReadTokens)
	}
	flow := saturatingAdd(input, u.OutputTokens)
	flow = saturatingAdd(flow, u.CacheCreation5mTokens)
	flow = saturatingAdd(flow, u.CacheCreation1hTokens)
	return flow
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

type CostBucket struct {
	RatePerToken   float64
	Tokens         int64
	AboveThreshold bool
}

func (c CostBucket) Cost() float64 {
	return c.RatePerToken * float64(c.Tokens)
}

type CostBuckets []CostBucket

func (c CostBuckets) Add(other CostBuckets) CostBuckets {
	result := append(CostBuckets(nil), c...)
	for _, addition := range other {
		matched := false
		for index := range result {
			if result[index].RatePerToken == addition.RatePerToken {
				result[index].Tokens = saturatingAdd(result[index].Tokens, addition.Tokens)
				result[index].AboveThreshold = result[index].AboveThreshold && addition.AboveThreshold
				matched = true
				break
			}
		}
		if !matched {
			result = append(result, addition)
		}
	}
	if len(result) == 0 {
		return nil
	}
	base := make(CostBuckets, 0, len(result))
	above := make(CostBuckets, 0, len(result))
	for _, bucket := range result {
		if bucket.AboveThreshold {
			above = append(above, bucket)
		} else {
			base = append(base, bucket)
		}
	}
	return append(base, above...)
}

func (c CostBuckets) Cost() float64 {
	var total float64
	for _, bucket := range c {
		total += bucket.Cost()
	}
	return total
}

func (c CostBuckets) TotalTokens() int64 {
	var total int64
	for _, bucket := range c {
		total = saturatingAdd(total, bucket.Tokens)
	}
	return total
}

type CostBreakdown struct {
	Input      CostBuckets
	Output     CostBuckets
	CacheWrite CostBuckets
	CacheRead  CostBuckets
}

func (c CostBreakdown) Add(other CostBreakdown) CostBreakdown {
	return CostBreakdown{
		Input:      c.Input.Add(other.Input),
		Output:     c.Output.Add(other.Output),
		CacheWrite: c.CacheWrite.Add(other.CacheWrite),
		CacheRead:  c.CacheRead.Add(other.CacheRead),
	}
}

func (c CostBreakdown) Clone() CostBreakdown {
	return CostBreakdown{
		Input:      append(CostBuckets(nil), c.Input...),
		Output:     append(CostBuckets(nil), c.Output...),
		CacheWrite: append(CostBuckets(nil), c.CacheWrite...),
		CacheRead:  append(CostBuckets(nil), c.CacheRead...),
	}
}

func (c CostBreakdown) Total() float64 {
	return c.Input.Cost() + c.Output.Cost() + c.CacheWrite.Cost() + c.CacheRead.Cost()
}

type EventKind string

const (
	EventUser          EventKind = "user"
	EventAssistantText EventKind = "assistant-text"
	EventThinking      EventKind = "thinking"
	EventToolCall      EventKind = "tool-call"
	EventToolResult    EventKind = "tool-result"
	EventSubagent      EventKind = "subagent"
	EventAdvisor       EventKind = "advisor"
	EventSystem        EventKind = "system"
	EventCompact       EventKind = "compact"
)

// ToolDetail is the full tool payload shown when a tool call is expanded.
type ToolDetail struct {
	Input  string // Full invocation; newlines preserved.
	Diff   string // Unified-diff body for edits, writes, and patches.
	Output string // Full result; newlines preserved.
}

// RecordRef locates the physical JSONL line an event came from.
type RecordRef struct {
	Path   string
	Offset int64
	Length int64
	Digest [32]byte
}

type Event struct {
	Timestamp     time.Time
	Kind          EventKind
	Text          string
	RecordRef     RecordRef `json:"-"`
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
	// Usage is the API request usage the log attributes to this event, set on the
	// single event that a billed request (Claude assistant line, Codex token_count)
	// produces so a turn can sum FlowTokens and read the last PromptTokens without
	// double counting. Nil for events without their own request.
	Usage *Usage
	// Cost is the priced breakdown of that same request, kept beside Usage so the
	// timeline can split input-side from output-side cost. Empty when Priced is
	// false. CostEstimated marks a rate estimate (always so for Codex).
	Cost          CostBreakdown
	Priced        bool
	CostEstimated bool
	// Harness marks a user-role record the agent harness injected rather than a
	// human typing: skill bodies, task notifications, compaction summaries, and
	// slash-command echoes. Both agents log these as user turns, so the timeline
	// needs the flag to label them apart.
	Harness bool
	// CompactTrigger and CompactPostTokens describe an EventCompact boundary: how
	// the compaction was invoked ("manual" or "auto") and the context token count
	// that survived it. The summarization request itself is unbilled and unlogged,
	// so a compaction carries no Usage. Empty and zero for every other event.
	CompactTrigger    string
	CompactPostTokens int64
}

type DuplicateOwner struct {
	SessionID string
	Title     string
	USD       float64
	Count     int
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
	AgentPath string
	ParentID  string
	HasError  bool
	Messages  int
	Usage     []Usage
	// Requests intentionally repeats Usage until all gross readers can derive their aggregates from the request ledger.
	Requests            []RequestUsage
	ModelCosts          map[string]float64
	ModelCostBreakdowns map[string]CostBreakdown
	Cost                Cost
	DuplicatedUSD       float64            `json:"-"`
	DuplicatedUsage     Usage              `json:"-"`
	DuplicatedCount     int                `json:"-"`
	DuplicatedByModel   map[string]float64 `json:"-"`
	DuplicatedOwners    []DuplicateOwner   `json:"-"`
	Events              []Event
	Subagents           []*Session
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

func (s Session) OwnedUsage() Usage {
	var total Usage
	for _, usage := range s.Usage {
		total = total.Add(usage)
	}
	total.InputTokens -= s.DuplicatedUsage.InputTokens
	total.OutputTokens -= s.DuplicatedUsage.OutputTokens
	total.CacheCreation5mTokens -= s.DuplicatedUsage.CacheCreation5mTokens
	total.CacheCreation1hTokens -= s.DuplicatedUsage.CacheCreation1hTokens
	total.CacheReadTokens -= s.DuplicatedUsage.CacheReadTokens
	for _, subagent := range s.Subagents {
		total = total.Add(subagent.OwnedUsage())
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

func (s Session) OwnedCost() Cost {
	total := s.Cost
	total.USD -= s.DuplicatedUSD
	seen := make(map[string]bool, len(total.MissingPricingModels))
	for _, name := range total.MissingPricingModels {
		seen[name] = true
	}
	for _, subagent := range s.Subagents {
		subtotal := subagent.OwnedCost()
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
