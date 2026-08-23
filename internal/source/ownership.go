package source

import (
	"context"
	"path/filepath"

	"github.com/motoki317/agtlog/internal/model"
)

type requestOwnershipKey struct {
	agent                      model.AgentKind
	messageID, requestID       string
	codexSummary               bool
	offset                     int64
	model, speed               string
	input, output              int64
	cacheCreation5m            int64
	cacheCreation1h, cacheRead int64
	inputIncludesCacheRead     bool
	hasLoggedCost              bool
	loggedCost                 float64
}

// AttributeOwnership applies the global-dedup idea from ccusage's adapter/claude/mod.rs
// (load_entries_inner, push_deduped_entry, and usage_dedupe_hash):
// https://github.com/ryoppippi/ccusage. agtlog assigns the earliest origin instead of
// whichever copy scan order encounters first.
func AttributeOwnership(sessions []*model.Session) {
	_ = attributeOwnershipContext(context.Background(), sessions)
}

func attributeOwnershipContext(ctx context.Context, sessions []*model.Session) error {
	owners := make(map[requestOwnershipKey]*model.Session)
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if session == nil {
			continue
		}
		session.DuplicatedUSD = 0
		session.DuplicatedUsage = model.Usage{}
		session.DuplicatedCount = 0
		session.DuplicatedByModel = nil
		session.DuplicatedOwners = nil
		for _, request := range session.Requests {
			if err := ctx.Err(); err != nil {
				return err
			}
			key, ok := ownershipKey(session, request)
			if !ok {
				continue
			}
			if owner := owners[key]; owner == nil || sessionStartedEarlier(session, owner) {
				owners[key] = session
			}
		}
	}

	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if session == nil {
			continue
		}
		ownerIndexes := make(map[*model.Session]int)
		for _, request := range session.Requests {
			if err := ctx.Err(); err != nil {
				return err
			}
			key, ok := ownershipKey(session, request)
			if !ok {
				continue
			}
			owner := owners[key]
			if owner == nil || owner == session {
				continue
			}
			session.DuplicatedUSD += request.USD
			session.DuplicatedUsage = session.DuplicatedUsage.Add(request.Usage)
			session.DuplicatedCount++
			if session.DuplicatedByModel == nil {
				session.DuplicatedByModel = make(map[string]float64)
			}
			session.DuplicatedByModel[request.Usage.Model] += request.USD
			index, exists := ownerIndexes[owner]
			if !exists {
				index = len(session.DuplicatedOwners)
				ownerIndexes[owner] = index
				session.DuplicatedOwners = append(session.DuplicatedOwners, model.DuplicateOwner{
					SessionID: owner.ID,
					Title:     owner.Title,
				})
			}
			session.DuplicatedOwners[index].USD += request.USD
			session.DuplicatedOwners[index].Count++
		}
	}
	return nil
}

func ownershipKey(session *model.Session, request model.RequestUsage) (requestOwnershipKey, bool) {
	key := requestOwnershipKey{agent: session.Agent, messageID: request.MessageID, requestID: request.RequestID}
	if request.MessageID != "" {
		return key, true
	}
	// Codex RequestUsage lacks the MessageID that normally gates ownership. Partial
	// mirrors can differ as files while sharing billed ledger entries, so stable usage fields form the key.
	if session.Agent != model.AgentCodex || session.ID == "" {
		return requestOwnershipKey{}, false
	}
	key.messageID = session.ID
	key.codexSummary = true
	key.offset = request.Offset
	key.model = request.Usage.Model
	key.speed = request.Usage.Speed
	key.input = request.Usage.InputTokens
	key.output = request.Usage.OutputTokens
	key.cacheCreation5m = request.Usage.CacheCreation5mTokens
	key.cacheCreation1h = request.Usage.CacheCreation1hTokens
	key.cacheRead = request.Usage.CacheReadTokens
	key.inputIncludesCacheRead = request.Usage.InputIncludesCacheRead
	if request.Usage.CostUSD != nil {
		key.hasLoggedCost = true
		key.loggedCost = *request.Usage.CostUSD
	}
	return key, true
}

func sessionStartedEarlier(candidate, current *model.Session) bool {
	if !candidate.StartedAt.Equal(current.StartedAt) {
		return candidate.StartedAt.Before(current.StartedAt)
	}
	if candidate.ID != current.ID {
		return candidate.ID < current.ID
	}
	return filepath.Clean(candidate.Path) < filepath.Clean(current.Path)
}
