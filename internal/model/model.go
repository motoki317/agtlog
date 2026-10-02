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
	Model                 string
	InputTokens           int64
	OutputTokens          int64
	CacheCreation5mTokens int64
	CacheCreation1hTokens int64
	CacheReadTokens       int64
	// InputIncludesCacheRead is true when InputTokens already counts
	// CacheReadTokens. Codex reports usage this way. Claude reports cache reads
	// apart from input.
	InputIncludesCacheRead bool
	Speed                  string
	CostUSD                *float64
}

type RequestUsage struct {
	MessageID string
	RequestID string
	// Offset is the byte offset of the source record when the adapter records
	// one. Codex uses a negative offset for an authoritative aggregate, which has
	// no physical record.
	Offset int64
	Usage  Usage
	USD    float64 `json:"-"`
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

// PromptTokens is the prompt size of one request. The API is stateless, so each
// request re-sends the whole conversation, and the prompt size equals the
// context window in use at that request.
func (u Usage) PromptTokens() int64 {
	prompt := u.InputTokens
	if !u.InputIncludesCacheRead {
		prompt = saturatingAdd(prompt, u.CacheReadTokens)
	}
	prompt = saturatingAdd(prompt, u.CacheCreation5mTokens)
	prompt = saturatingAdd(prompt, u.CacheCreation1hTokens)
	return prompt
}

// FlowTokens counts the tokens that one request adds: uncached input, cache
// writes, and output. Cache reads re-send earlier context, so FlowTokens
// excludes them. PromptTokens counts the whole context instead.
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
	EstimatedRates       []EstimatedRate
	MissingPricingModels []string
}

// EstimatedRate records a logged Model that agtlog priced at the published rate
// of PricingModel. The name of the stand-in lets a reader review the estimate.
type EstimatedRate struct {
	Model        string
	PricingModel string
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
	EventUsage         EventKind = "usage"
)

// ToolDetail is the full tool payload that an expanded tool call shows.
type ToolDetail struct {
	Input  string // Full invocation with its newlines.
	Diff   string // Unified diff body for edits, writes, and patches.
	Output string // Full result with its newlines.
}

// RecordRef locates the physical JSONL line that produced an event.
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
	Model         string
	CallID        string
	ToolName      string
	ToolInput     string
	ResultSummary string
	Detail        *ToolDetail
	Duration      time.Duration
	AgentID       string
	Subagent      *Session
	// Usage comes from the billed request that produced this event: a Claude
	// assistant line or a Codex token_count. Only one event per request
	// carries it, so a turn can sum FlowTokens and read PromptTokens from its
	// last request without double counting. Usage is nil for an event without
	// its own request.
	Usage *Usage
	// Cost is the priced breakdown of the same request. The timeline uses it to
	// split input-side cost from output-side cost. Cost is empty when Priced is
	// false. CostEstimated marks a substituted rate. PricingModel names the
	// published stand-in, and it is empty when Model has its own rate.
	Cost          CostBreakdown
	Priced        bool
	CostEstimated bool
	PricingModel  string `json:"-"`
	// Harness marks a user-role record that the agent harness injected, not text
	// that a person typed: skill bodies, task notifications, compaction
	// summaries, and slash-command echoes. Both agents log these records as user
	// turns, so the timeline needs the flag to label them.
	Harness bool
	// CompactTrigger ("manual" or "auto") and CompactPostTokens, the context size
	// in tokens after the compaction, describe an EventCompact boundary. The log
	// has no usage for the summarization request, so a compaction carries no
	// Usage. Both fields keep their zero values for every other event.
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
	ID    string
	Agent AgentKind
	Path  string
	// SourceSize is the byte length that summary parsing read. Detail loading
	// reads no further. Zero means that the bound is unknown.
	SourceSize int64
	CWD        string
	Project    string
	Title      string
	Models     []string
	StartedAt  time.Time
	UpdatedAt  time.Time
	GitBranch  string
	AgentPath  string
	ParentID   string
	// SpawnCallID is the ID of the parent's tool call that started this
	// subagent. Empty means that it is unknown.
	SpawnCallID string
	HasError    bool
	Messages    int
	ToolCalls   int
	Usage       []Usage
	// Requests is the billed-request ledger that the summary cache stores. Codex
	// stores one entry per request for a clean partition. Otherwise it stores one
	// authoritative aggregate per model.
	Requests            []RequestUsage
	ModelCosts          map[string]float64       `json:"-"`
	ModelCostBreakdowns map[string]CostBreakdown `json:"-"`
	Cost                Cost                     `json:"-"`
	DuplicatedUSD       float64                  `json:"-"`
	DuplicatedUsage     Usage                    `json:"-"`
	DuplicatedCount     int                      `json:"-"`
	DuplicatedByModel   map[string]float64       `json:"-"`
	DuplicatedOwners    []DuplicateOwner         `json:"-"`
	Events              []Event
	Group               bool
	Subagents           []*Session
}

func (s Session) Turns() int {
	return s.Messages + s.ToolCalls
}

func (s Session) TotalTurns() int {
	total := s.Turns()
	for _, subagent := range s.Subagents {
		total += subagent.TotalTurns()
	}
	return total
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

func (s Session) DescendantAgentCount() int {
	total := 0
	for _, subagent := range s.Subagents {
		total += subagent.DescendantAgentCount()
		if !subagent.Group {
			total++
		}
	}
	return total
}

func (s Session) OwnedUsage() Usage {
	total := s.OwnedSelfUsage()
	return total.Add(s.OwnedDescendantUsage())
}

func (s Session) OwnedSelfUsage() Usage {
	var total Usage
	for _, usage := range s.Usage {
		total = total.Add(usage)
	}
	total.InputTokens -= s.DuplicatedUsage.InputTokens
	total.OutputTokens -= s.DuplicatedUsage.OutputTokens
	total.CacheCreation5mTokens -= s.DuplicatedUsage.CacheCreation5mTokens
	total.CacheCreation1hTokens -= s.DuplicatedUsage.CacheCreation1hTokens
	total.CacheReadTokens -= s.DuplicatedUsage.CacheReadTokens
	return total
}

func (s Session) OwnedDescendantUsage() Usage {
	var total Usage
	for _, subagent := range s.Subagents {
		total = total.Add(subagent.OwnedUsage())
	}
	return total
}

func (s Session) TotalCost() Cost {
	return addSubagentCosts(s.Cost, s.Subagents, (*Session).TotalCost)
}

func (s Session) OwnedCost() Cost {
	return addSubagentCosts(s.OwnedSelfCost(), s.Subagents, (*Session).OwnedCost)
}

func (s Session) OwnedSelfCost() Cost {
	total := s.Cost.clone()
	total.USD -= s.DuplicatedUSD
	return total
}

func (s Session) OwnedDescendantCost() Cost {
	return addSubagentCosts(Cost{}, s.Subagents, (*Session).OwnedCost)
}

// addSubagentCosts appends to a copy of the flag slices in base, so the result
// never shares a backing array with a session.
func addSubagentCosts(base Cost, subagents []*Session, subtotalOf func(*Session) Cost) Cost {
	total := base.clone()
	estimated := make(map[EstimatedRate]bool, len(total.EstimatedRates))
	for _, rate := range total.EstimatedRates {
		estimated[rate] = true
	}
	seen := make(map[string]bool, len(total.MissingPricingModels))
	for _, name := range total.MissingPricingModels {
		seen[name] = true
	}
	for _, subagent := range subagents {
		subtotal := subtotalOf(subagent)
		total.USD += subtotal.USD
		total.Estimated = total.Estimated || subtotal.Estimated
		for _, rate := range subtotal.EstimatedRates {
			if !estimated[rate] {
				total.EstimatedRates = append(total.EstimatedRates, rate)
				estimated[rate] = true
			}
		}
		for _, name := range subtotal.MissingPricingModels {
			if !seen[name] {
				total.MissingPricingModels = append(total.MissingPricingModels, name)
				seen[name] = true
			}
		}
	}
	return total
}

func (c Cost) clone() Cost {
	c.EstimatedRates = append([]EstimatedRate(nil), c.EstimatedRates...)
	c.MissingPricingModels = append([]string(nil), c.MissingPricingModels...)
	return c
}
