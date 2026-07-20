package codex

import (
	"context"
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
	return "codex-parser-v18:" + p.defaultPricingModel + ":" + p.calculator.Fingerprint()
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type tokenUsageRecord struct {
	model string
	usage tokenUsage
}

type logRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type           string `json:"type"`
		Model          string `json:"model"`
		ID             string `json:"id"`
		SessionID      string `json:"session_id"`
		CWD            string `json:"cwd"`
		AgentPath      string `json:"agent_path"`
		AgentThreadID  string `json:"agent_thread_id"`
		ParentThreadID string `json:"parent_thread_id"`
		Kind           string `json:"kind"`
		ThreadSource   string `json:"thread_source"`
		Git            struct {
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
	var pricingRecords []tokenUsageRecord
	hasLast := false
	seenModels := make(map[string]bool)
	metaSeen := false
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
			!(envelope.Type == "event_msg" && (envelope.Payload.Type == "user_message" || envelope.Payload.Type == "agent_message" || envelope.Payload.Type == "sub_agent_activity" || envelope.Payload.Type == "token_count")) {
			return
		}
		var record logRecord
		if json.Unmarshal(line, &record) != nil {
			return
		}
		// Newer Codex embeds the parent's session_meta after the child's in subagent sidecars.
		if record.Type == "session_meta" && !metaSeen {
			metaSeen = true
			session.ID = record.Payload.ID
			if session.ID == "" {
				session.ID = record.Payload.SessionID
			}
			session.CWD = record.Payload.CWD
			session.Project = filepath.Base(record.Payload.CWD)
			session.GitBranch = record.Payload.Git.Branch
			session.AgentPath = record.Payload.AgentPath
			session.ParentID = record.Payload.ParentThreadID
		}
		if record.Type == "turn_context" && record.Payload.Model != "" {
			currentModel = record.Payload.Model
			if !seenModels[currentModel] {
				session.Models = append(session.Models, currentModel)
				seenModels[currentModel] = true
			}
		}
		// Messages counts this session's own conversation turns (user prompts +
		// agent replies), matching the user+assistant message lines the detail
		// timeline shows. Ceiling: a subagent-heavy run undercounts, since work
		// delegated to subagents surfaces in the timeline through the deduplicated
		// bridge, not as event_msg turns here; the recursive size lives in TOKENS.
		if record.Type == "event_msg" && (record.Payload.Type == "user_message" || record.Payload.Type == "agent_message") {
			if session.Title == "" && record.Payload.Type == "user_message" {
				var user userMessageRecord
				if json.Unmarshal(line, &user) == nil {
					session.Title = titleFromUserMessage(user.Payload.Message)
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
			pricingRecords = append(pricingRecords, tokenUsageRecord{model: currentModel, usage: *record.Payload.Info.Last})
			hasLast = true
		}
	})
	if err != nil {
		return nil, err
	}
	// Per-turn pricing is safe only when Codex's cumulative total confirms that
	// the Last records form a clean partition; every other path stays lumped.
	cleanPartition := lastTotal != nil && hasLast && summedLast == *lastTotal
	if lastTotal != nil && (!hasLast || summedLast != *lastTotal) {
		usageByModel = map[string]*tokenUsage{lastTotalModel: lastTotal}
		usageOrder = []string{lastTotalModel}
	}
	if !cleanPartition {
		pricingRecords = pricingRecords[:0]
		for _, usageModel := range usageOrder {
			pricingRecords = append(pricingRecords, tokenUsageRecord{model: usageModel, usage: *usageByModel[usageModel]})
		}
	}
	priceableModels := make(map[string]bool, len(usageOrder))
	for _, usageModel := range usageOrder {
		usage, ok := codexUsage(usageModel, *usageByModel[usageModel])
		if !ok {
			continue
		}
		session.Usage = append(session.Usage, usage)
		priceableModels[usageModel] = true
	}
	missingPricing := make(map[string]bool)
	for _, record := range pricingRecords {
		if !priceableModels[record.model] {
			continue
		}
		usage, ok := codexUsage(record.model, record.usage)
		if !ok {
			continue
		}
		calculated := p.calculator.CalculateCodex(usage, p.defaultPricingModel)
		if session.ModelCosts == nil {
			session.ModelCosts = make(map[string]float64)
		}
		session.ModelCosts[usage.Model] += calculated.USD
		if p.calculator.HasCodexPricing(usage, p.defaultPricingModel) {
			if session.ModelCostBreakdowns == nil {
				session.ModelCostBreakdowns = make(map[string]model.CostBreakdown)
			}
			current := session.ModelCostBreakdowns[usage.Model]
			session.ModelCostBreakdowns[usage.Model] = current.Add(p.calculator.BreakdownCodex(usage, p.defaultPricingModel))
		}
		session.Cost.USD += calculated.USD
		session.Cost.Estimated = true
		for _, name := range calculated.MissingPricingModels {
			if !missingPricing[name] {
				session.Cost.MissingPricingModels = append(session.Cost.MissingPricingModels, name)
				missingPricing[name] = true
			}
		}
	}
	if session.AgentPath != "" {
		session.Title = model.CleanTitle(filepath.Base(session.AgentPath))
	}
	return session, nil
}

func codexUsage(usageModel string, selected tokenUsage) (model.Usage, bool) {
	if selected.ReasoningOutputTokens > math.MaxInt64-selected.OutputTokens {
		return model.Usage{}, false
	}
	return model.Usage{
		Model:                  usageModel,
		InputTokens:            selected.InputTokens,
		OutputTokens:           selected.OutputTokens + selected.ReasoningOutputTokens,
		CacheReadTokens:        selected.CachedInputTokens,
		InputIncludesCacheRead: true,
	}, true
}

func titleFromUserMessage(message string) string {
	task := codexTimelineUserMessage(message)
	if task == "" {
		task = message
	}
	var fallback, skippedTag string
	for _, line := range strings.Split(task, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if skippedTag != "" {
			if strings.Contains(lower, "</"+skippedTag+">") {
				skippedTag = ""
			}
			continue
		}
		if tag := codexPreambleTag(line); tag != "" {
			if !strings.Contains(lower, "</"+tag+">") {
				skippedTag = tag
			}
			continue
		}
		heading := ""
		if strings.HasPrefix(line, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
		if strings.HasPrefix(strings.ToLower(heading), "brief") {
			heading = strings.TrimLeft(heading[len("brief"):], " —-:")
			if title := model.CleanTitle(heading); title != "" {
				return title
			}
		}
		candidate := line
		if heading != "" {
			candidate = heading
		}
		if fallback == "" && !codexTitleBoilerplate(candidate) {
			fallback = model.CleanTitle(candidate)
		}
	}
	return fallback
}

func codexPreambleTag(line string) string {
	for _, tag := range []string{"recommended_plugins", "instructions", "environment_context"} {
		prefix := "<" + tag
		if len(line) >= len(prefix) && strings.EqualFold(line[:len(prefix)], prefix) {
			return tag
		}
	}
	return ""
}

func codexPreambleUserMessage(text string) bool {
	line := codexFirstNonemptyLine(text)
	if line == "" {
		return false
	}
	if codexPreambleTag(line) != "" {
		return true
	}
	title := strings.ToLower(model.CleanTitle(line))
	return strings.HasPrefix(title, codexAutonomousAgentPreamble) || strings.HasPrefix(title, codexAdvisorPreamble)
}

func codexTimelineUserMessage(text string) string {
	text = strings.TrimSpace(text)
	for text != "" {
		if tag := codexPreambleTag(text); tag != "" {
			closing := "</" + tag + ">"
			index := codexIndexEqualFold(text, closing)
			if index < 0 {
				return ""
			}
			text = strings.TrimSpace(text[index+len(closing):])
			continue
		}
		line, rest, _ := strings.Cut(text, "\n")
		line = strings.TrimSpace(line)
		if codexPreambleUserMessage(text) {
			return codexTaskAfterDelimiter(text)
		}
		if codexWrapperBoilerplateLine(line) {
			text = strings.TrimSpace(rest)
			continue
		}
		return text
	}
	return ""
}

func codexIndexEqualFold(text, target string) int {
	for offset := 0; offset+len(target) <= len(text); {
		index := strings.IndexByte(text[offset:], target[0])
		if index < 0 {
			return -1
		}
		index += offset
		if index+len(target) > len(text) {
			return -1
		}
		if strings.EqualFold(text[index:index+len(target)], target) {
			return index
		}
		offset = index + 1
	}
	return -1
}

func codexTaskAfterDelimiter(text string) string {
	for text != "" {
		line, rest, found := strings.Cut(text, "\n")
		if strings.TrimSpace(line) == "---" {
			return codexTimelineUserMessage(rest)
		}
		if !found {
			break
		}
		text = rest
	}
	return ""
}

func codexWrapperBoilerplateLine(line string) bool {
	title := strings.ToLower(model.CleanTitle(line))
	return strings.HasPrefix(title, "agents.md instructions") && codexTitleBoilerplate(line)
}

func codexFirstNonemptyLine(text string) string {
	for {
		line, rest, found := strings.Cut(text, "\n")
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
		if !found {
			return ""
		}
		text = rest
	}
}

const (
	codexAutonomousAgentPreamble = "you are an autonomous implementation agent"
	codexAdvisorPreamble         = "you are an independent, cross-model reviewer"
)

func codexTitleBoilerplate(line string) bool {
	title := strings.ToLower(model.CleanTitle(line))
	if title == "" || title == "task" || title == "requirements" || title == "available plugins" {
		return true
	}
	for _, prefix := range []string{
		"agents.md instructions", codexAutonomousAgentPreamble, codexAdvisorPreamble,
		"deliver the complete implementation", "work from the task below",
		"you receive no conversation history", "the orchestrator accepts or rejects",
		"here is a list of plugins", "before the first substantive task",
	} {
		if strings.HasPrefix(title, prefix) {
			return true
		}
	}
	return false
}

func (p Parser) LoadEvents(ctx context.Context, session *model.Session) error {
	return p.loadEvents(ctx, session)
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
	parts := agentPathParts(agentPath)
	base := agentPathParts(root.AgentPath)
	if len(base) > 0 {
		if len(parts) <= len(base) {
			return
		}
		for index := range base {
			if parts[index] != base[index] {
				return
			}
		}
		parts = parts[len(base):]
	}
	if len(parts) == 0 {
		return
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
			childParts := append(append([]string(nil), base...), parts[:index+1]...)
			child = &model.Session{
				ID:        name,
				Agent:     model.AgentCodex,
				Path:      sourcePath + "#" + strings.Join(parts[:index+1], "/"),
				CWD:       root.CWD,
				Project:   root.Project,
				Title:     name,
				AgentPath: "/root/" + strings.Join(childParts, "/"),
				StartedAt: timestamp,
				UpdatedAt: timestamp,
			}
			parent.Subagents = append(parent.Subagents, child)
		}
		if index == len(parts)-1 && threadID != "" {
			child.ID = threadID
			if rollout := siblingRollout(sourcePath, threadID); rollout != "" {
				child.Path = rollout
			}
		}
		parent = child
	}
}

func agentPathParts(agentPath string) []string {
	if len(agentPath) > maxAgentPathBytes {
		return nil
	}
	parts := strings.Split(strings.Trim(agentPath, "/"), "/")
	if len(parts) > 0 && parts[0] == "root" {
		parts = parts[1:]
	}
	if len(parts) == 0 || len(parts) > maxAgentDepth {
		return nil
	}
	for _, part := range parts {
		if part == "" || len(part) > maxAgentComponent {
			return nil
		}
	}
	return parts
}

func siblingRollout(sourcePath, threadID string) string {
	if threadID == "" || len(threadID) > maxAgentComponent || strings.ContainsAny(threadID, `/\\`) {
		return ""
	}
	entries, err := os.ReadDir(filepath.Dir(sourcePath))
	if err != nil {
		return ""
	}
	want := threadID + ".jsonl"
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), want) {
			continue
		}
		return filepath.Join(filepath.Dir(sourcePath), entry.Name())
	}
	return ""
}
