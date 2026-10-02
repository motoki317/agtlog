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

var listSortFields = []string{"updated", "started", "tokens", "cost", "turns", "messages"}

type listOptions struct {
	common  commonOptions
	project string
	cwd     string
	query   string
	since   string
	until   string
	window  updateWindow
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
	filtered := filterListSessions(sessions, options)
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
	flags.StringVar(&options.project, "project", "", "keep sessions whose project basename is this name")
	flags.StringVar(&options.cwd, "cwd", "", "keep sessions whose working directory is this path or below it")
	flags.StringVar(&options.query, "query", "", "fuzzy-match agent, project, and title")
	flags.StringVar(&options.since, "since", "", "keep sessions updated at or after this time: RFC 3339, YYYY-MM-DD, or a duration such as 7d")
	flags.StringVar(&options.until, "until", "", "keep sessions updated at or before this time: RFC 3339, YYYY-MM-DD, or a duration such as 7d")
	flags.StringVar(&options.sort, "sort", "updated", "sort by updated, started, tokens, cost, turns, or messages")
	flags.StringVar(&options.order, "order", "desc", "sort order: asc or desc")
	flags.IntVar(&options.limit, "limit", 50, "maximum sessions to return")
	flags.IntVar(&options.offset, "offset", 0, "sessions to skip")
	flags.BoolVar(&options.all, "all", false, "return every matching session")
	if _, err := parseFlexible(flags, args, ""); err != nil {
		return listOptions{}, err
	}
	if err := options.common.validate(); err != nil {
		return listOptions{}, err
	}
	if !slices.Contains(listSortFields, options.sort) {
		return listOptions{}, usageError(fmt.Sprintf("invalid sort %q: use one of %s", options.sort, strings.Join(listSortFields, ", ")))
	}
	if options.order != "asc" && options.order != "desc" {
		return listOptions{}, usageError(fmt.Sprintf("invalid order %q: use asc or desc", options.order))
	}
	if options.limit <= 0 {
		return listOptions{}, usageError("--limit must be greater than zero")
	}
	if options.offset < 0 {
		return listOptions{}, usageError("--offset must not be negative")
	}
	window, err := parseUpdateWindow(options.since, options.until, time.Now(), time.Local)
	if err != nil {
		return listOptions{}, err
	}
	options.window = window
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

func filterListSessions(sessions []*model.Session, options listOptions) []*model.Session {
	candidates := make([]*model.Session, 0, len(sessions))
	for _, session := range sessions {
		if options.common.agent != "" && string(session.Agent) != options.common.agent || options.project != "" && session.Project != options.project || options.cwd != "" && !cwdContains(options.cwd, session.CWD) || !options.window.contains(session.UpdatedAt) {
			continue
		}
		candidates = append(candidates, session)
	}
	query := model.SessionFilterQuery(options.query)
	if query == "" {
		return candidates
	}
	haystacks := make([]string, len(candidates))
	for index, session := range candidates {
		haystacks[index] = model.SessionFilterText(session)
	}
	matches := fuzzy.FindNoSort(query, haystacks)
	result := make([]*model.Session, 0, len(matches))
	for _, match := range matches {
		result = append(result, candidates[match.Index])
	}
	return result
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
		case "turns":
			result = cmp.Compare(left.TotalTurns(), right.TotalTurns())
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
