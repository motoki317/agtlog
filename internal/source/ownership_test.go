package source

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

type ownershipTestSource struct {
	sessions map[string]*model.Session
}

func (s ownershipTestSource) Agent() model.AgentKind   { return model.AgentClaude }
func (s ownershipTestSource) CacheFingerprint() string { return "test-ownership-parser-v1" }
func (s ownershipTestSource) Roots() []string          { return nil }
func (s ownershipTestSource) Discover(context.Context) ([]string, error) {
	paths := make([]string, 0, len(s.sessions))
	for path := range s.sessions {
		paths = append(paths, path)
	}
	return paths, nil
}
func (s ownershipTestSource) Parse(path string) (*model.Session, error) { return s.sessions[path], nil }
func (s ownershipTestSource) ParseContext(_ context.Context, path string) (*model.Session, error) {
	return s.Parse(path)
}
func (s ownershipTestSource) Reprice(*model.Session) {}

func TestAttributeOwnershipAssignsSharedRequestToEarliestSession(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{
		MessageID: "message-shared",
		RequestID: "request-shared",
		Usage:     model.Usage{Model: "claude-fable-5", InputTokens: 10, OutputTokens: 2},
		USD:       0.25,
	}
	origin := &model.Session{
		ID: "session-origin", Agent: model.AgentClaude, Title: "Origin",
		StartedAt: started, Requests: []model.RequestUsage{request},
	}
	replay := &model.Session{
		ID: "session-replay", Agent: model.AgentClaude, Title: "Replay",
		StartedAt: started.Add(time.Minute), Requests: []model.RequestUsage{request},
	}

	AttributeOwnership([]*model.Session{replay, origin})

	if origin.DuplicatedCount != 0 {
		t.Fatalf("origin duplicated count = %d, want 0", origin.DuplicatedCount)
	}
	wantUsage := model.Usage{InputTokens: 10, OutputTokens: 2}
	if replay.DuplicatedUSD != 0.25 || replay.DuplicatedCount != 1 ||
		!reflect.DeepEqual(replay.DuplicatedUsage, wantUsage) ||
		!reflect.DeepEqual(replay.DuplicatedByModel, map[string]float64{"claude-fable-5": 0.25}) {
		t.Fatalf("replay attribution = %#v, want shared request duplicated", replay)
	}
	wantOwners := []model.DuplicateOwner{{SessionID: origin.ID, Title: origin.Title, USD: 0.25, Count: 1}}
	if !reflect.DeepEqual(replay.DuplicatedOwners, wantOwners) {
		t.Fatalf("replay owners = %#v, want %#v", replay.DuplicatedOwners, wantOwners)
	}
}

func TestAttributeOwnershipBreaksEqualStartTieBySessionID(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared", USD: 0.25}
	largerID := &model.Session{
		ID: "session-z", Agent: model.AgentClaude, StartedAt: started,
		Requests: []model.RequestUsage{request},
	}
	smallerID := &model.Session{
		ID: "session-a", Agent: model.AgentClaude, StartedAt: started,
		Requests: []model.RequestUsage{request},
	}

	AttributeOwnership([]*model.Session{largerID, smallerID})

	if smallerID.DuplicatedCount != 0 || largerID.DuplicatedCount != 1 ||
		len(largerID.DuplicatedOwners) != 1 || largerID.DuplicatedOwners[0].SessionID != smallerID.ID {
		t.Fatalf("equal-start attribution = smaller %#v, larger %#v; want smallest ID owner", smallerID, largerID)
	}
}

func TestAttributeOwnershipDeduplicatesSharedCodexLedgerInPartialMirrors(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	sharedUsage := model.Usage{Model: "gpt-5.6-sol", InputTokens: 10, OutputTokens: 2}
	uniqueUsage := model.Usage{Model: "gpt-5.6-sol", InputTokens: 3, OutputTokens: 1}
	shared := model.RequestUsage{Offset: 128, Usage: sharedUsage, USD: 0.25}
	unique := model.RequestUsage{Offset: 256, Usage: uniqueUsage, USD: 0.125}
	firstPath := &model.Session{
		ID: "session-shared", Agent: model.AgentCodex, Path: "/archive/a/session.jsonl", StartedAt: started,
		Usage: []model.Usage{sharedUsage}, Requests: []model.RequestUsage{shared}, Cost: model.Cost{USD: shared.USD},
	}
	secondPath := &model.Session{
		ID: "session-shared", Agent: model.AgentCodex, Path: "/archive/z/session.jsonl", StartedAt: started, Title: "extended copy",
		Usage: []model.Usage{sharedUsage, uniqueUsage}, Requests: []model.RequestUsage{shared, unique}, Cost: model.Cost{USD: shared.USD + unique.USD},
	}

	AttributeOwnership([]*model.Session{secondPath, firstPath})

	if firstPath.DuplicatedCount != 0 || secondPath.DuplicatedCount != 1 ||
		secondPath.OwnedUsage().TotalTokens() != uniqueUsage.TotalTokens() || secondPath.OwnedCost().USD != unique.USD {
		t.Fatalf("partial Codex mirror attribution = first %#v, second %#v; want only the shared ledger entry deducted", firstPath, secondPath)
	}
}

func TestAttributeOwnershipDoesNotMergeDifferentCodexLedgerEntries(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	first := &model.Session{
		ID: "session-shared", Agent: model.AgentCodex, Path: "/archive/a/session.jsonl", StartedAt: started,
		Requests: []model.RequestUsage{{Offset: 128, Usage: model.Usage{Model: "gpt-5.6-sol", InputTokens: 10}}},
	}
	second := &model.Session{
		ID: "session-shared", Agent: model.AgentCodex, Path: "/archive/b/session.jsonl", StartedAt: started,
		Requests: []model.RequestUsage{{Offset: 128, Usage: model.Usage{Model: "gpt-5.6-sol", InputTokens: 11}}},
	}

	AttributeOwnership([]*model.Session{first, second})

	if first.DuplicatedCount != 0 || second.DuplicatedCount != 0 {
		t.Fatalf("different Codex ledgers duplicated counts = %d/%d, want 0/0", first.DuplicatedCount, second.DuplicatedCount)
	}
}

func TestAttributeOwnershipUsesFullCompositeRequestIdentity(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		originAgent   model.AgentKind
		replayAgent   model.AgentKind
		originRequest model.RequestUsage
		replayRequest model.RequestUsage
	}{
		{
			name:        "request ID differs",
			originAgent: model.AgentClaude, replayAgent: model.AgentClaude,
			originRequest: model.RequestUsage{MessageID: "message-shared", RequestID: "request-origin", USD: 0.25},
			replayRequest: model.RequestUsage{MessageID: "message-shared", RequestID: "request-replay", USD: 0.25},
		},
		{
			name:        "message ID differs",
			originAgent: model.AgentClaude, replayAgent: model.AgentClaude,
			originRequest: model.RequestUsage{MessageID: "message-origin", RequestID: "request-shared", USD: 0.25},
			replayRequest: model.RequestUsage{MessageID: "message-replay", RequestID: "request-shared", USD: 0.25},
		},
		{
			name:        "agent differs",
			originAgent: model.AgentClaude, replayAgent: model.AgentCodex,
			originRequest: model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared", USD: 0.25},
			replayRequest: model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared", USD: 0.25},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			origin := &model.Session{
				ID: "session-origin", Agent: test.originAgent, StartedAt: started,
				Requests: []model.RequestUsage{test.originRequest},
			}
			replay := &model.Session{
				ID: "session-replay", Agent: test.replayAgent, StartedAt: started.Add(time.Minute),
				Requests: []model.RequestUsage{test.replayRequest},
			}

			AttributeOwnership([]*model.Session{origin, replay})

			if origin.DuplicatedCount != 0 || replay.DuplicatedCount != 0 {
				t.Fatalf("distinct composite keys duplicated counts = %d/%d, want 0/0", origin.DuplicatedCount, replay.DuplicatedCount)
			}
		})
	}
}

func TestAttributeOwnershipSubtractsOnlySharedRequests(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	shared := model.RequestUsage{
		MessageID: "message-shared", RequestID: "request-shared",
		Usage: model.Usage{Model: "claude-fable-5", InputTokens: 10}, USD: 2,
	}
	unique := model.RequestUsage{
		MessageID: "message-unique", RequestID: "request-unique",
		Usage: model.Usage{Model: "claude-fable-5", InputTokens: 20}, USD: 3,
	}
	origin := &model.Session{
		ID: "session-origin", Agent: model.AgentClaude, StartedAt: started,
		Usage: []model.Usage{shared.Usage}, Cost: model.Cost{USD: shared.USD},
		Requests: []model.RequestUsage{shared},
	}
	replay := &model.Session{
		ID: "session-replay", Agent: model.AgentClaude, StartedAt: started.Add(time.Minute),
		Usage: []model.Usage{shared.Usage, unique.Usage}, Cost: model.Cost{USD: shared.USD + unique.USD},
		Requests: []model.RequestUsage{shared, unique},
	}

	AttributeOwnership([]*model.Session{origin, replay})

	if replay.DuplicatedCount != 1 || replay.OwnedCost().USD != unique.USD ||
		replay.OwnedUsage().InputTokens != unique.Usage.InputTokens {
		t.Fatalf("partial-overlap attribution = %#v, want only unique request owned", replay)
	}
}

func TestAttributeOwnershipAggregatesMultipleOwnersAndModels(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	first := model.RequestUsage{
		MessageID: "message-first", RequestID: "request-first",
		Usage: model.Usage{Model: "model-a", InputTokens: 10, OutputTokens: 1}, USD: 0.25,
	}
	second := model.RequestUsage{
		MessageID: "message-second", RequestID: "request-second",
		Usage: model.Usage{Model: "model-b", InputTokens: 20, CacheReadTokens: 2}, USD: 0.50,
	}
	third := model.RequestUsage{
		MessageID: "message-third", RequestID: "request-third",
		Usage: model.Usage{Model: "model-a", OutputTokens: 3, CacheCreation5mTokens: 4}, USD: 0.75,
	}
	firstOwner := &model.Session{
		ID: "session-first", Agent: model.AgentClaude, Title: "First origin", StartedAt: started,
		Requests: []model.RequestUsage{first, second},
	}
	secondOwner := &model.Session{
		ID: "session-second", Agent: model.AgentClaude, Title: "Second origin", StartedAt: started.Add(time.Minute),
		Requests: []model.RequestUsage{third},
	}
	replay := &model.Session{
		ID: "session-replay", Agent: model.AgentClaude, StartedAt: started.Add(2 * time.Minute),
		Requests: []model.RequestUsage{first, second, third},
	}

	AttributeOwnership([]*model.Session{replay, secondOwner, firstOwner})

	wantUsage := first.Usage.Add(second.Usage).Add(third.Usage)
	wantByModel := map[string]float64{"model-a": 1, "model-b": 0.50}
	wantOwners := []model.DuplicateOwner{
		{SessionID: firstOwner.ID, Title: firstOwner.Title, USD: 0.75, Count: 2},
		{SessionID: secondOwner.ID, Title: secondOwner.Title, USD: 0.75, Count: 1},
	}
	if replay.DuplicatedUSD != 1.50 || replay.DuplicatedCount != 3 ||
		!reflect.DeepEqual(replay.DuplicatedUsage, wantUsage) ||
		!reflect.DeepEqual(replay.DuplicatedByModel, wantByModel) ||
		!reflect.DeepEqual(replay.DuplicatedOwners, wantOwners) {
		t.Fatalf("aggregate attribution = %#v, want usage %#v, models %#v, owners %#v", replay, wantUsage, wantByModel, wantOwners)
	}
}

func TestAttributeOwnershipDeduplicatesNWayReplay(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared", USD: 0.25}
	sessions := []*model.Session{
		{ID: "session-third", Agent: model.AgentClaude, StartedAt: started.Add(2 * time.Minute), Requests: []model.RequestUsage{request}},
		{ID: "session-origin", Agent: model.AgentClaude, StartedAt: started, Requests: []model.RequestUsage{request}},
		{ID: "session-second", Agent: model.AgentClaude, StartedAt: started.Add(time.Minute), Requests: []model.RequestUsage{request}},
	}

	AttributeOwnership(sessions)

	if sessions[1].DuplicatedCount != 0 || sessions[0].DuplicatedCount != 1 || sessions[2].DuplicatedCount != 1 {
		t.Fatalf("N-way duplicated counts = %d/%d/%d, want 1/0/1", sessions[0].DuplicatedCount, sessions[1].DuplicatedCount, sessions[2].DuplicatedCount)
	}
	for _, replay := range []*model.Session{sessions[0], sessions[2]} {
		if len(replay.DuplicatedOwners) != 1 || replay.DuplicatedOwners[0].SessionID != sessions[1].ID {
			t.Fatalf("N-way replay owners = %#v, want origin %q", replay.DuplicatedOwners, sessions[1].ID)
		}
	}
}

func TestAttributeOwnershipNeverDeduplicatesEmptyMessageID(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{RequestID: "request-shared", USD: 0.25}
	origin := &model.Session{
		ID: "session-origin", Agent: model.AgentClaude, StartedAt: started,
		Requests: []model.RequestUsage{request},
	}
	replay := &model.Session{
		ID: "session-replay", Agent: model.AgentClaude, StartedAt: started.Add(time.Minute),
		Requests: []model.RequestUsage{request},
	}

	AttributeOwnership([]*model.Session{origin, replay})

	if origin.DuplicatedCount != 0 || replay.DuplicatedCount != 0 {
		t.Fatalf("empty-message duplicated counts = %d/%d, want 0/0", origin.DuplicatedCount, replay.DuplicatedCount)
	}
}

func TestAttributeOwnershipIsIdempotent(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{
		MessageID: "message-shared", RequestID: "request-shared",
		Usage: model.Usage{Model: "claude-fable-5", InputTokens: 10}, USD: 0.25,
	}
	origin := &model.Session{
		ID: "session-origin", Agent: model.AgentClaude, StartedAt: started,
		Requests: []model.RequestUsage{request},
	}
	replay := &model.Session{
		ID: "session-replay", Agent: model.AgentClaude, StartedAt: started.Add(time.Minute),
		Requests: []model.RequestUsage{request},
	}
	sessions := []*model.Session{origin, replay}

	AttributeOwnership(sessions)
	first := *replay
	AttributeOwnership(sessions)

	if replay.DuplicatedUSD != first.DuplicatedUSD ||
		!reflect.DeepEqual(replay.DuplicatedUsage, first.DuplicatedUsage) ||
		replay.DuplicatedCount != first.DuplicatedCount ||
		!reflect.DeepEqual(replay.DuplicatedByModel, first.DuplicatedByModel) ||
		!reflect.DeepEqual(replay.DuplicatedOwners, first.DuplicatedOwners) {
		t.Fatalf("second attribution = %#v, want first attribution %#v", replay, &first)
	}
}

func TestAttributeOwnershipReattributesWhenEarlierSessionAppears(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared", USD: 0.25}
	currentOwner := &model.Session{
		ID: "session-current", Agent: model.AgentClaude, StartedAt: started.Add(time.Minute),
		Requests: []model.RequestUsage{request},
	}
	replay := &model.Session{
		ID: "session-replay", Agent: model.AgentClaude, StartedAt: started.Add(2 * time.Minute),
		Requests: []model.RequestUsage{request},
	}
	AttributeOwnership([]*model.Session{currentOwner, replay})
	if currentOwner.DuplicatedCount != 0 || len(replay.DuplicatedOwners) != 1 ||
		replay.DuplicatedOwners[0].SessionID != currentOwner.ID {
		t.Fatalf("initial attribution = current %#v, replay %#v", currentOwner, replay)
	}

	earlier := &model.Session{
		ID: "session-earlier", Agent: model.AgentClaude, StartedAt: started,
		Requests: []model.RequestUsage{request},
	}
	AttributeOwnership([]*model.Session{currentOwner, replay, earlier})

	if earlier.DuplicatedCount != 0 || currentOwner.DuplicatedCount != 1 || replay.DuplicatedCount != 1 ||
		len(currentOwner.DuplicatedOwners) != 1 || len(replay.DuplicatedOwners) != 1 ||
		currentOwner.DuplicatedOwners[0].SessionID != earlier.ID ||
		replay.DuplicatedOwners[0].SessionID != earlier.ID {
		t.Fatalf("reattribution = earlier %#v, current %#v, replay %#v", earlier, currentOwner, replay)
	}
}

func TestRegistryDiscoverAttributesTopLevelOwnership(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared", USD: 0.25}
	origin := &model.Session{
		ID: "session-origin", Agent: model.AgentClaude, Path: "/fictional/origin.jsonl", StartedAt: started,
		Requests: []model.RequestUsage{request},
	}
	replay := &model.Session{
		ID: "session-replay", Agent: model.AgentClaude, Path: "/fictional/replay.jsonl", StartedAt: started.Add(time.Minute),
		Requests: []model.RequestUsage{request},
	}
	adapter := ownershipTestSource{sessions: map[string]*model.Session{origin.Path: origin, replay.Path: replay}}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 2})

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	discoveredOrigin := sessionWithID(sessions, origin.ID)
	discoveredReplay := sessionWithID(sessions, replay.ID)
	if len(sessions) != 2 || discoveredOrigin == nil || discoveredReplay == nil || discoveredOrigin.DuplicatedCount != 0 || discoveredReplay.DuplicatedCount != 1 {
		t.Fatalf("discovered attribution = sessions %d, origin %#v, replay %#v", len(sessions), discoveredOrigin, discoveredReplay)
	}
}

func TestRegistryDiscoverExcludesLinkedSubagentsFromOwnership(t *testing.T) {
	started := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	request := model.RequestUsage{MessageID: "message-shared", RequestID: "request-shared", USD: 0.25}
	childPath := "/fictional/parent.jsonl#worker"
	parent := &model.Session{
		ID: "session-parent", Agent: model.AgentClaude, Path: "/fictional/parent.jsonl", StartedAt: started,
		Subagents: []*model.Session{{ID: "session-child", Path: childPath}},
	}
	child := &model.Session{
		ID: "session-child", ParentID: parent.ID, Agent: model.AgentClaude, Path: childPath,
		StartedAt: started, Requests: []model.RequestUsage{request},
	}
	topLevel := &model.Session{
		ID: "session-top", Agent: model.AgentClaude, Path: "/fictional/top.jsonl",
		StartedAt: started.Add(time.Minute), Requests: []model.RequestUsage{request},
	}
	adapter := ownershipTestSource{sessions: map[string]*model.Session{
		parent.Path:   parent,
		child.Path:    child,
		topLevel.Path: topLevel,
	}}
	registry := NewRegistry([]Source{adapter}, Options{Workers: 2})

	sessions, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	discoveredParent := sessionWithID(sessions, parent.ID)
	discoveredTopLevel := sessionWithID(sessions, topLevel.ID)
	if len(sessions) != 2 || discoveredParent == nil || discoveredTopLevel == nil || len(discoveredParent.Subagents) != 1 || discoveredParent.Subagents[0].ID != child.ID {
		t.Fatalf("discovered graph = %#v, want two roots with linked child", sessions)
	}
	discoveredChild := discoveredParent.Subagents[0]
	if discoveredChild.DuplicatedCount != 0 || discoveredTopLevel.DuplicatedCount != 0 {
		t.Fatalf("subagent leaked into top-level ownership: child %#v, top-level %#v", discoveredChild, discoveredTopLevel)
	}
}
