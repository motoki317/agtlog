package cli

import (
	"math"
	"slices"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

const SchemaVersion = 1

const (
	fieldText        = "text"
	fieldToolInput   = "tool.input"
	fieldToolDiff    = "tool.diff"
	fieldToolOutput  = "tool.output"
	fieldToolSummary = "tool.summary"
)

var wireEventKinds = []model.EventKind{
	model.EventUser,
	model.EventAssistantText,
	model.EventThinking,
	model.EventToolCall,
	model.EventToolResult,
	model.EventSubagent,
	model.EventAdvisor,
	model.EventSystem,
	model.EventCompact,
	model.EventUsage,
}

func wireEventKind(kind model.EventKind) (string, bool) {
	if !slices.Contains(wireEventKinds, kind) {
		return "", false
	}
	return string(kind), true
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Ref     string `json:"ref,omitempty"`
	Path    string `json:"path,omitempty"`
}

type TokenTotals struct {
	UncachedInput int64 `json:"uncached_input"`
	Output        int64 `json:"output"`
	CacheWrite    int64 `json:"cache_write"`
	CacheRead     int64 `json:"cache_read"`
	Total         int64 `json:"total"`
}

type Cost struct {
	USD            float64  `json:"usd"`
	Complete       bool     `json:"complete"`
	Estimated      bool     `json:"estimated"`
	MissingPricing []string `json:"missing_pricing"`
}

type Session struct {
	Ref       string      `json:"ref"`
	Agent     string      `json:"agent"`
	Project   string      `json:"project"`
	CWD       string      `json:"cwd"`
	Title     string      `json:"title"`
	GitBranch string      `json:"git_branch"`
	Models    []string    `json:"models"`
	StartedAt string      `json:"started_at"`
	UpdatedAt string      `json:"updated_at"`
	Messages  int         `json:"messages"`
	Subagents int         `json:"subagents"`
	HasError  bool        `json:"has_error"`
	Tokens    TokenTotals `json:"tokens"`
	Cost      Cost        `json:"cost"`
	Path      string      `json:"path"`
}

type ListPage struct {
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	Returned   int  `json:"returned"`
	Total      int  `json:"total"`
	HasMore    bool `json:"has_more"`
	NextOffset int  `json:"next_offset"`
}

type ListResponse struct {
	SchemaVersion int       `json:"schema_version"`
	Command       string    `json:"command"`
	Sessions      []Session `json:"sessions"`
	Page          ListPage  `json:"page"`
	Warnings      []Warning `json:"warnings"`
}

type ScopedTokens struct {
	Self        TokenTotals `json:"self"`
	Descendants TokenTotals `json:"descendants"`
	Total       TokenTotals `json:"total"`
}

type ScopedCost struct {
	Self        Cost `json:"self"`
	Descendants Cost `json:"descendants"`
	Total       Cost `json:"total"`
}

type ShowTotals struct {
	Tokens ScopedTokens `json:"tokens"`
	Cost   ScopedCost   `json:"cost"`
}

type EventTool struct {
	Name       string `json:"name"`
	CallID     string `json:"call_id"`
	Summary    string `json:"summary"`
	Input      string `json:"input"`
	Diff       string `json:"diff"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
}

type EventUsage struct {
	UncachedInput int64 `json:"uncached_input"`
	Output        int64 `json:"output"`
	CacheWrite    int64 `json:"cache_write"`
	CacheRead     int64 `json:"cache_read"`
	Flow          int64 `json:"flow"`
	Context       int64 `json:"context"`
}

type EventCost struct {
	USD       float64 `json:"usd"`
	Complete  bool    `json:"complete"`
	Estimated bool    `json:"estimated"`
}

type EventRecord struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type Compact struct {
	Trigger    string `json:"trigger"`
	PostTokens int64  `json:"post_tokens"`
}

type Event struct {
	Index          int          `json:"index"`
	Timestamp      string       `json:"timestamp"`
	Kind           string       `json:"kind"`
	Text           string       `json:"text"`
	Model          string       `json:"model"`
	Tool           *EventTool   `json:"tool,omitempty"`
	Usage          *EventUsage  `json:"usage,omitempty"`
	Cost           *EventCost   `json:"cost,omitempty"`
	Truncated      []string     `json:"truncated"`
	Record         *EventRecord `json:"record,omitempty"`
	Harness        bool         `json:"harness,omitempty"`
	SubagentRef    string       `json:"subagent_ref,omitempty"`
	Compact        *Compact     `json:"compact,omitempty"`
	UsageAggregate bool         `json:"usage_aggregate,omitempty"`
}

type ShowPage struct {
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	Returned   int  `json:"returned"`
	Total      int  `json:"total"`
	HasMore    bool `json:"has_more"`
	NextOffset int  `json:"next_offset"`
	Complete   bool `json:"complete"`
}

type ShowResponse struct {
	SchemaVersion int        `json:"schema_version"`
	Command       string     `json:"command"`
	Session       Session    `json:"session"`
	SubagentRefs  []string   `json:"subagent_refs"`
	Totals        ShowTotals `json:"totals"`
	Events        []Event    `json:"events"`
	Page          ShowPage   `json:"page"`
	Warnings      []Warning  `json:"warnings"`
}

type RawRecord struct {
	Index   int    `json:"index"`
	Path    string `json:"path"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
	RawJSON string `json:"raw_json"`
}

type RawResponse struct {
	SchemaVersion int       `json:"schema_version"`
	Command       string    `json:"command"`
	RawRecord     RawRecord `json:"raw_record"`
	Warnings      []Warning `json:"warnings"`
}

type HitSession struct {
	Ref       string `json:"ref"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
}

type HitEvent struct {
	Index     int    `json:"index"`
	Timestamp string `json:"timestamp"`
	Kind      string `json:"kind"`
	Tool      string `json:"tool"`
}

type SearchHit struct {
	Session HitSession `json:"session"`
	Event   HitEvent   `json:"event"`
	Field   string     `json:"field"`
	Range   [2]int     `json:"range"`
	Snippet string     `json:"snippet"`
	Matches int        `json:"matches"`
}

type SearchPage struct {
	Offset          int  `json:"offset"`
	Limit           int  `json:"limit"`
	Returned        int  `json:"returned"`
	HasMore         bool `json:"has_more"`
	NextOffset      int  `json:"next_offset"`
	Complete        bool `json:"complete"`
	Total           *int `json:"total,omitempty"`
	SessionsScanned int  `json:"sessions_scanned"`
	SessionsMatched int  `json:"sessions_matched"`
}

type SearchResponse struct {
	SchemaVersion int         `json:"schema_version"`
	Command       string      `json:"command"`
	Hits          []SearchHit `json:"hits"`
	Page          SearchPage  `json:"page"`
	Warnings      []Warning   `json:"warnings"`
}

type ErrorCandidate struct {
	Ref       string `json:"ref"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updated_at"`
}

type ErrorDetail struct {
	Code       string           `json:"code"`
	Message    string           `json:"message"`
	Candidates []ErrorCandidate `json:"candidates,omitempty"`
}

type ErrorResponse struct {
	SchemaVersion int         `json:"schema_version"`
	Error         ErrorDetail `json:"error"`
}

func sessionDTO(session *model.Session, ref string) Session {
	models := append([]string(nil), session.Models...)
	slices.Sort(models)
	return Session{
		Ref:       ref,
		Agent:     string(session.Agent),
		Project:   session.Project,
		CWD:       session.CWD,
		Title:     session.Title,
		GitBranch: session.GitBranch,
		Models:    nonNil(models),
		StartedAt: timestamp(session.StartedAt),
		UpdatedAt: timestamp(session.UpdatedAt),
		Messages:  session.Messages,
		Subagents: descendantCount(session),
		HasError:  session.HasError,
		Tokens:    tokenTotals(session.OwnedUsage()),
		Cost:      costDTO(session.OwnedCost()),
		Path:      session.Path,
	}
}

func tokenTotals(usage model.Usage) TokenTotals {
	input := usage.InputTokens
	if usage.InputIncludesCacheRead {
		input = max(0, input-usage.CacheReadTokens)
	}
	cacheWrite := usage.CacheCreation5mTokens + usage.CacheCreation1hTokens
	return TokenTotals{
		UncachedInput: input,
		Output:        usage.OutputTokens,
		CacheWrite:    cacheWrite,
		CacheRead:     usage.CacheReadTokens,
		Total:         usage.TotalTokens(),
	}
}

func costDTO(value model.Cost) Cost {
	missing := append([]string(nil), value.MissingPricingModels...)
	slices.Sort(missing)
	usd := value.USD
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		usd = 0
	}
	return Cost{
		USD:            usd,
		Complete:       len(missing) == 0,
		Estimated:      value.Estimated,
		MissingPricing: nonNil(missing),
	}
}

func descendantCount(session *model.Session) int {
	total := 0
	for _, child := range session.Subagents {
		total += 1 + descendantCount(child)
	}
	return total
}

func timestamp(value time.Time) string {
	return value.Format(time.RFC3339Nano)
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
