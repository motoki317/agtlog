package claude

import (
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
	out := make([]usageRecord, 0, len(records))
	indices := make(map[[32]byte]int)
	for _, record := range records {
		if record.MessageID == "" {
			out = append(out, record)
			continue
		}
		key := sha256.Sum256([]byte(record.MessageID + "\x00" + record.RequestID))
		index, exists := indices[key]
		if !exists {
			indices[key] = len(out)
			out = append(out, record)
			continue
		}
		if betterRecord(record, out[index]) {
			out[index] = record
		}
	}
	return out
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
