package tui

import (
	"strings"
	"testing"

	"github.com/motoki317/agtlog/internal/model"
)

func BenchmarkDetailBulkExpansion(b *testing.B) {
	events := []model.Event{{Kind: model.EventUser, Text: "Inspect the fictional route"}}
	body := strings.Repeat("fictional route remains clear\n", 10)
	for range 1_999 {
		events = append(events, model.Event{
			Kind: model.EventToolCall, ToolName: "exec_command", ToolInput: "check-route",
			Detail: &model.ToolDetail{Input: "check-route", Output: body},
		})
	}
	detail := newDetailState(&model.Session{ID: "lunar", Agent: model.AgentCodex, Events: events}, 160, 40, newStyles(Theme{Name: "mono"}))

	b.Run("expand", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			detail.setAllExpanded(false)
			b.StartTimer()
			detail.setAllExpanded(true)
		}
	})
	b.Run("collapse", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			detail.setAllExpanded(true)
			b.StartTimer()
			detail.setAllExpanded(false)
		}
	})
}
