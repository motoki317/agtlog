package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

var textNow = time.Now

func writeText(output io.Writer, value any) error {
	switch response := value.(type) {
	case ListResponse:
		return writeListText(output, response)
	case ShowResponse:
		return writeShowText(output, response)
	case RawResponse:
		return writeRawText(output, response)
	case SearchResponse:
		return writeSearchText(output, response)
	default:
		return fmt.Errorf("text renderer unavailable for %T", value)
	}
}

func writeListText(output io.Writer, response ListResponse) error {
	table := tabwriter.NewWriter(output, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "REF\tAGENT\tPROJECT\tTITLE\tAGE\tMSGS\tSUBS\tTOKENS\t$"); err != nil {
		return err
	}
	for _, session := range response.Sessions {
		subs := "-"
		if session.Subagents > 0 {
			subs = fmt.Sprint(session.Subagents)
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			terminalSafe(session.Ref), terminalSafe(session.Agent), terminalSafe(session.Project), terminalSafe(session.Title),
			textAge(session.UpdatedAt), session.Messages, subs, humanTokens(session.Tokens.Total), humanCost(session.Cost)); err != nil {
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
		terminalSafe(response.Session.Ref), terminalSafe(response.Session.Agent), terminalSafe(response.Session.Project),
		terminalSafe(response.Session.Title), humanTokens(response.Session.Tokens.Total), humanCost(response.Session.Cost)); err != nil {
		return err
	}
	for _, ref := range response.SubagentRefs {
		if _, err := fmt.Fprintf(output, "SUBAGENT\t%s\n", terminalSafe(ref)); err != nil {
			return err
		}
	}
	for _, event := range response.Events {
		text := terminalSafe(event.Text)
		if event.Tool != nil && event.Tool.Summary != "" {
			text += " -> " + terminalSafe(event.Tool.Summary)
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
		if _, err := fmt.Fprintf(output, "[%d]\t%s\t%s\t%s\n", event.Index, textClock(event.Timestamp), terminalSafe(event.Kind), text); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(output, "PAGE\treturned=%d\ttotal=%d\thas_more=%t\tnext_offset=%d\tcomplete=%t\n",
		response.Page.Returned, response.Page.Total, response.Page.HasMore, response.Page.NextOffset, response.Page.Complete); err != nil {
		return err
	}
	return writeWarningText(output, response.Warnings)
}

func writeRawText(output io.Writer, response RawResponse) error {
	if _, err := fmt.Fprintf(output, "[%d]\t%s\t%d\t%d\n%s\n", response.RawRecord.Index, terminalSafe(response.RawRecord.Path), response.RawRecord.Offset, response.RawRecord.Length, terminalSafe(response.RawRecord.RawJSON)); err != nil {
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
			terminalSafe(hit.Session.Ref), hit.Event.Index, terminalSafe(hit.Event.Kind), terminalSafe(hit.Field),
			hit.Range[0], hit.Range[1], hit.Matches, terminalSafe(hit.Snippet)); err != nil {
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
		if _, err := fmt.Fprintf(output, "WARNING\t%s\t%s\t%s\n", terminalSafe(warning.Code), terminalSafe(location), terminalSafe(warning.Message)); err != nil {
			return err
		}
	}
	return nil
}

func terminalSafe(value string) string {
	value = ansi.Strip(value)
	var sanitized strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			r = ' '
		}
		sanitized.WriteRune(r)
	}
	return strings.Join(strings.Fields(sanitized.String()), " ")
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
	switch {
	case abs >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case abs >= 1_000_000:
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
