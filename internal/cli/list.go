package cli

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/motoki317/agtlog/internal/model"
	"github.com/sahilm/fuzzy"
)

type listOptions struct {
	common  commonOptions
	project string
	cwd     string
	query   string
	since   string
	until   string
	sort    string
	order   string
	limit   int
	offset  int
	all     bool
}

func runList(ctx context.Context, args []string, help io.Writer, factory RegistryFactory) (any, string, error) {
	options, err := parseListOptions(args, help)
	if errorsIsHelp(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	registry, err := factory(ctx, options.common.registryOptions())
	if err != nil {
		return nil, "", runtimeError("internal", err.Error())
	}
	sessions, diagnostics, err := registry.DiscoverWithDiagnostics(ctx)
	if err != nil {
		return nil, "", runtimeError("internal", err.Error())
	}
	sessions, commandDiags := addressableRoots(sessions, commandDiagnostics(diagnostics))
	now := time.Now()
	filtered, err := filterListSessions(sessions, options, now, time.Local)
	if err != nil {
		return nil, "", err
	}
	sortListSessions(filtered, options.sort, options.order)
	total := len(filtered)
	start := min(options.offset, total)
	end := total
	if !options.all && options.limit < total-start {
		end = start + options.limit
	}
	rows := make([]Session, 0, end-start)
	for _, session := range filtered[start:end] {
		rows = append(rows, sessionDTO(session, canonicalRootRef(session)))
	}
	next := options.offset + len(rows)
	pageLimit := options.limit
	if options.all {
		pageLimit = 0
	}
	return ListResponse{
		SchemaVersion: SchemaVersion,
		Command:       "list",
		Sessions:      nonNil(rows),
		Page: ListPage{
			Offset:     options.offset,
			Limit:      pageLimit,
			Returned:   len(rows),
			Total:      total,
			HasMore:    next < total,
			NextOffset: next,
		},
		Warnings: discoveryWarnings(commandDiags),
	}, options.common.format, nil
}

func parseListOptions(args []string, help io.Writer) (listOptions, error) {
	options := listOptions{sort: "updated", order: "desc", limit: 50}
	flags := newFlagSet("agtlog list", help, listUsage)
	addCommonFlags(flags, &options.common)
	flags.StringVar(&options.project, "project", "", "match the project basename")
	flags.StringVar(&options.cwd, "cwd", "", "match a working directory and its descendants")
	flags.StringVar(&options.query, "query", "", "fuzzy-match agent, project, and title")
	flags.StringVar(&options.since, "since", "", "minimum update time: RFC3339, local date, or duration")
	flags.StringVar(&options.until, "until", "", "maximum update time: RFC3339, local date, or duration")
	flags.StringVar(&options.sort, "sort", "updated", "sort by updated, started, tokens, cost, or messages")
	flags.StringVar(&options.order, "order", "desc", "sort order: asc or desc")
	flags.IntVar(&options.limit, "limit", 50, "maximum sessions to return")
	flags.IntVar(&options.offset, "offset", 0, "sessions to skip")
	flags.BoolVar(&options.all, "all", false, "return every matching session")
	if _, err := parseFlexible(flags, args, 0); err != nil {
		return listOptions{}, err
	}
	if err := options.common.validate(); err != nil {
		return listOptions{}, err
	}
	if !slices.Contains([]string{"updated", "started", "tokens", "cost", "messages"}, options.sort) {
		return listOptions{}, usageError(fmt.Sprintf("invalid sort %q", options.sort))
	}
	if options.order != "asc" && options.order != "desc" {
		return listOptions{}, usageError(fmt.Sprintf("invalid order %q", options.order))
	}
	if options.limit <= 0 {
		return listOptions{}, usageError("--limit must be greater than zero")
	}
	if options.offset < 0 {
		return listOptions{}, usageError("--offset must not be negative")
	}
	return options, nil
}

func listUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: agtlog list [flags]")
	_, _ = fmt.Fprintln(output, "Lists top-level sessions as indented JSON by default.")
	_, _ = fmt.Fprintln(output, "Use --format text for a terminal-safe table.")
}

func errorsIsHelp(err error) bool {
	return err == flag.ErrHelp
}

func filterListSessions(sessions []*model.Session, options listOptions, now time.Time, location *time.Location) ([]*model.Session, error) {
	var since, until time.Time
	var err error
	if options.since != "" {
		since, err = parseTimeFilter(options.since, now, location)
		if err != nil {
			return nil, err
		}
	}
	if options.until != "" {
		until, err = parseTimeFilter(options.until, now, location)
		if err != nil {
			return nil, err
		}
	}
	candidates := make([]*model.Session, 0, len(sessions))
	for _, session := range sessions {
		if options.common.agent != "" && string(session.Agent) != options.common.agent || options.project != "" && session.Project != options.project || options.cwd != "" && !cwdContains(options.cwd, session.CWD) || !since.IsZero() && session.UpdatedAt.Before(since) || !until.IsZero() && session.UpdatedAt.After(until) {
			continue
		}
		candidates = append(candidates, session)
	}
	query := strings.ToLower(strings.TrimSpace(options.query))
	if query == "" {
		return candidates, nil
	}
	haystacks := make([]string, len(candidates))
	for index, session := range candidates {
		haystacks[index] = strings.ToLower(strings.Join([]string{string(session.Agent), session.Project, session.Title}, " "))
	}
	matches := fuzzy.FindNoSort(query, haystacks)
	result := make([]*model.Session, 0, len(matches))
	for _, match := range matches {
		result = append(result, candidates[match.Index])
	}
	return result, nil
}

func sortListSessions(sessions []*model.Session, field, order string) {
	slices.SortStableFunc(sessions, func(left, right *model.Session) int {
		result := 0
		switch field {
		case "started":
			result = left.StartedAt.Compare(right.StartedAt)
		case "tokens":
			result = cmp.Compare(left.OwnedUsage().TotalTokens(), right.OwnedUsage().TotalTokens())
		case "cost":
			result = cmp.Compare(costDTO(left.OwnedCost()).USD, costDTO(right.OwnedCost()).USD)
		case "messages":
			result = cmp.Compare(left.Messages, right.Messages)
		default:
			result = left.UpdatedAt.Compare(right.UpdatedAt)
		}
		if order == "desc" {
			result = -result
		}
		if result != 0 {
			return result
		}
		if left.Agent != right.Agent {
			return cmp.Compare(left.Agent, right.Agent)
		}
		return strings.Compare(left.ID, right.ID)
	})
}

func canonicalRootRef(session *model.Session) string {
	return string(session.Agent) + ":" + escapeRefComponent(session.ID)
}
