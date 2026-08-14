package source

import (
	"context"

	"github.com/motoki317/agtlog/internal/model"
)

type requestOwnershipKey struct {
	agent                model.AgentKind
	messageID, requestID string
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
			if request.MessageID == "" {
				continue
			}
			key := requestOwnershipKey{agent: session.Agent, messageID: request.MessageID, requestID: request.RequestID}
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
			if request.MessageID == "" {
				continue
			}
			key := requestOwnershipKey{agent: session.Agent, messageID: request.MessageID, requestID: request.RequestID}
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

func sessionStartedEarlier(candidate, current *model.Session) bool {
	if !candidate.StartedAt.Equal(current.StartedAt) {
		return candidate.StartedAt.Before(current.StartedAt)
	}
	return candidate.ID < current.ID
}
