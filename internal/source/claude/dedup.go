package claude

import (
	"context"
	"crypto/sha256"

	"github.com/motoki317/agtlog/internal/model"
)

type usageRecord struct {
	MessageID   string
	RequestID   string
	IsSidechain bool
	Usage       model.Usage
}

func deduplicate(records []usageRecord) []usageRecord {
	deduplicated, _ := deduplicateContext(context.Background(), records)
	return deduplicated
}

func deduplicateContext(ctx context.Context, records []usageRecord) ([]usageRecord, error) {
	out := make([]usageRecord, 0, len(records))
	indices := make(map[[32]byte]int)
	firstByMessage := make(map[string]int)
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if record.MessageID == "" {
			out = append(out, record)
			continue
		}
		key := sha256.Sum256([]byte(record.MessageID + "\x00" + record.RequestID))
		index, exists := indices[key]
		if !exists && record.RequestID == "" {
			index, exists = firstByMessage[record.MessageID]
		}
		if !exists && record.RequestID != "" {
			if candidate, ok := firstByMessage[record.MessageID]; ok && out[candidate].RequestID == "" {
				index, exists = candidate, true
			}
		}
		if !exists {
			indices[key] = len(out)
			if _, ok := firstByMessage[record.MessageID]; !ok {
				firstByMessage[record.MessageID] = len(out)
			}
			out = append(out, record)
			continue
		}
		if betterRecord(record, out[index]) {
			out[index] = record
		}
		indices[key] = index
	}
	return out, nil
}

func betterRecord(candidate, current usageRecord) bool {
	if candidate.IsSidechain != current.IsSidechain {
		return !candidate.IsSidechain
	}
	if candidate.Usage.TotalTokens() != current.Usage.TotalTokens() {
		return candidate.Usage.TotalTokens() > current.Usage.TotalTokens()
	}
	return candidate.Usage.Speed != "" && current.Usage.Speed == ""
}
