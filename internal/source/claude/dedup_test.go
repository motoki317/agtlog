package claude

import (
	"testing"

	"github.com/motoki317/agtlog/internal/model"
)

func TestDeduplicatePrefersNonSidechainRecord(t *testing.T) {
	records := []usageRecord{
		{MessageID: "message-a", RequestID: "request-a", IsSidechain: true, Usage: model.Usage{InputTokens: 20}},
		{MessageID: "message-a", RequestID: "request-a", Usage: model.Usage{InputTokens: 10}},
	}

	got := deduplicate(records)
	if len(got) != 1 || got[0].IsSidechain {
		t.Fatalf("deduplicate() = %#v, want one non-sidechain record", got)
	}
}

func TestDeduplicatePrefersHigherTokenTotal(t *testing.T) {
	records := []usageRecord{
		{MessageID: "message-a", RequestID: "request-a", Usage: model.Usage{InputTokens: 10}},
		{MessageID: "message-a", RequestID: "request-a", Usage: model.Usage{InputTokens: 20}},
	}

	got := deduplicate(records)
	if len(got) != 1 || got[0].Usage.InputTokens != 20 {
		t.Fatalf("deduplicate() = %#v, want higher-token record", got)
	}
}

func TestDeduplicatePrefersRecordWithSpeed(t *testing.T) {
	records := []usageRecord{
		{MessageID: "message-a", RequestID: "request-a", Usage: model.Usage{InputTokens: 10}},
		{MessageID: "message-a", RequestID: "request-a", Usage: model.Usage{InputTokens: 10, Speed: "fast"}},
	}

	got := deduplicate(records)
	if len(got) != 1 || got[0].Usage.Speed != "fast" {
		t.Fatalf("deduplicate() = %#v, want record with speed", got)
	}
}

func TestDeduplicateUsesSidechainFallbackWhenRequestIDDiffers(t *testing.T) {
	records := []usageRecord{
		{MessageID: "message-a", IsSidechain: true, Usage: model.Usage{InputTokens: 20}},
		{MessageID: "message-a", RequestID: "request-a", Usage: model.Usage{InputTokens: 10}},
	}

	got := deduplicate(records)
	if len(got) != 1 || got[0].IsSidechain || got[0].RequestID != "request-a" {
		t.Fatalf("deduplicate() = %#v, want one non-sidechain request-a record", got)
	}
}

func TestDeduplicateKeepsDistinctNonemptyRequestIDs(t *testing.T) {
	records := []usageRecord{
		{MessageID: "message-a", RequestID: "request-a"},
		{MessageID: "message-a", RequestID: "request-b"},
	}

	if got := deduplicate(records); len(got) != 2 {
		t.Fatalf("deduplicate() = %#v, want two distinct requests", got)
	}
}

func TestDeduplicateNeverMergesRecordsWithoutMessageID(t *testing.T) {
	records := []usageRecord{{RequestID: "request-a"}, {RequestID: "request-a"}}

	if got := deduplicate(records); len(got) != 2 {
		t.Fatalf("deduplicate() = %#v, want two records", got)
	}
}
