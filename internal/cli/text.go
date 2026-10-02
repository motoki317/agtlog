package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/motoki317/agtlog/internal/model"
)

var textNow = time.Now

func writeText(output io.Writer, value any) error {
	switch response := value.(type) {
	case ListResponse:
		return writeListText(output, response)
	case ShowResponse:
		return writeShowText(output, response)
	case SearchResponse:
		return writeSearchText(output, response)
	default:
		return fmt.Errorf("text renderer unavailable for %T", value)
	}
}

func writeListText(output io.Writer, response ListResponse) error {
	table := tabwriter.NewWriter(output, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "REF\tAGENT\tPROJECT\tTITLE\tAGE\tTURNS\tSUBS\tTOKENS\tCOST"); err != nil {
		return err
	}
	for _, session := range response.Sessions {
		subs := "-"
		if session.Subagents > 0 {
			subs = fmt.Sprint(session.Subagents)
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			model.TerminalLine(session.Ref, 0), model.TerminalLine(session.Agent, 0), model.TerminalLine(session.Project, 0), model.TerminalLine(session.Title, 0),
			textAge(session.UpdatedAt), session.Turns, subs, humanTokens(session.Tokens.Total), humanCost(session.Cost)); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "PAGE\treturned=%d\ttotal=%d\thas_more=%t\tnext_offset=%d\n",
		response.Page.Returned, response.Page.Total, response.Page.HasMore, response.Page.NextOffset); err != nil {
		return err
	}
	return writeWarningText(output, response.Warnings)
}

func writeShowText(output io.Writer, response ShowResponse) error {
	if _, err := fmt.Fprintf(output, "REF\t%s\nAGENT\t%s\nPROJECT\t%s\nTITLE\t%s\nTOKENS\t%s\nCOST\t%s\n",
		model.TerminalLine(response.Session.Ref, 0), model.TerminalLine(response.Session.Agent, 0), model.TerminalLine(response.Session.Project, 0),
		model.TerminalLine(response.Session.Title, 0), humanTokens(response.Session.Tokens.Total), humanCost(response.Session.Cost)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "TURNS\t%d\tself=%d\tdescendants=%d\n", response.Totals.Turns.Total, response.Totals.Turns.Self, response.Totals.Turns.Descendants); err != nil {
		return err
	}
	for _, ref := range response.SubagentRefs {
		if _, err := fmt.Fprintf(output, "SUBAGENT\t%s\n", model.TerminalLine(ref, 0)); err != nil {
			return err
		}
	}
	for _, event := range response.Events {
		text := model.TerminalLine(event.Text, 0)
		if event.Tool != nil && event.Tool.Summary != "" {
			text += " -> " + model.TerminalLine(event.Tool.Summary, 0)
		}
		var metrics []string
		if event.Tool != nil && event.Tool.DurationMS > 0 {
			metrics = append(metrics, fmt.Sprintf("%.1fs", float64(event.Tool.DurationMS)/1000))
		}
		if event.Usage != nil {
			metrics = append(metrics, "ctx "+humanTokens(event.Usage.Context))
		}
		if len(metrics) > 0 {
			text += "  " + strings.Join(metrics, "  ")
		}
		if _, err := fmt.Fprintf(output, "[%d]\t%s\t%s\t%s\n", event.Index, textClock(event.Timestamp), model.TerminalLine(event.Kind, 0), text); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(output, "PAGE\treturned=%d\ttotal=%d\thas_more=%t\tnext_offset=%d\tcomplete=%t\n",
		response.Page.Returned, response.Page.Total, response.Page.HasMore, response.Page.NextOffset, response.Page.Complete); err != nil {
		return err
	}
	return writeWarningText(output, response.Warnings)
}

func writeSearchText(output io.Writer, response SearchResponse) error {
	table := tabwriter.NewWriter(output, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "REF\tINDEX\tKIND\tFIELD\tRANGE\tMATCHES\tSNIPPET"); err != nil {
		return err
	}
	for _, hit := range response.Hits {
		if _, err := fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%d:%d\t%d\t%s\n",
			model.TerminalLine(hit.Session.Ref, 0), hit.Event.Index, model.TerminalLine(hit.Event.Kind, 0), model.TerminalLine(hit.Field, 0),
			hit.Range[0], hit.Range[1], hit.Matches, model.TerminalLine(hit.Snippet, 0)); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	total := "-"
	if response.Page.Total != nil {
		total = fmt.Sprint(*response.Page.Total)
	}
	if _, err := fmt.Fprintf(output, "PAGE\treturned=%d\ttotal=%s\thas_more=%t\tnext_offset=%d\tcomplete=%t\tsessions_scanned=%d\tsessions_matched=%d\n",
		response.Page.Returned, total, response.Page.HasMore, response.Page.NextOffset, response.Page.Complete,
		response.Page.SessionsScanned, response.Page.SessionsMatched); err != nil {
		return err
	}
	return writeWarningText(output, response.Warnings)
}

func writeWarningText(output io.Writer, warnings []Warning) error {
	for _, warning := range warnings {
		location := warning.Ref
		if location == "" {
			location = warning.Path
		}
		if _, err := fmt.Fprintf(output, "WARNING\t%s\t%s\t%s\n", model.TerminalLine(warning.Code, 0), model.TerminalLine(location, 0), model.TerminalLine(warning.Message, 0)); err != nil {
			return err
		}
	}
	return nil
}

func textAge(value string) string {
	updated, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || updated.IsZero() {
		return "-"
	}
	age := textNow().Sub(updated)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

func textClock(value string) string {
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "--:--:--"
	}
	return timestamp.Format("15:04:05")
}

func humanTokens(value int64) string {
	abs := value
	if abs < 0 {
		abs = -abs
	}
	// Each threshold is the smallest value that the next smaller unit rounds to
	// 1000, so 999,500 renders as 1.0M and not 1000k.
	switch {
	case abs >= 999_950_000:
		return fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case abs >= 999_500:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%.0fk", float64(value)/1_000)
	default:
		return fmt.Sprint(value)
	}
}

func humanCost(cost Cost) string {
	prefix, suffix := "", ""
	if cost.Estimated {
		prefix = "~"
	}
	if !cost.Complete {
		suffix = "!"
	}
	return fmt.Sprintf("%s%.2f%s", prefix, cost.USD, suffix)
}
