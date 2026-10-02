package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/motoki317/agtlog/internal/model"
)

type searchOptions struct {
	common        commonOptions
	project       string
	cwd           string
	since         string
	until         string
	window        updateWindow
	kind          string
	session       string
	regex         bool
	caseSensitive bool
	limit         int
	offset        int
	all           bool
	snippet       int
}

type searchCandidate struct {
	index int
	start graphNode
	nodes []graphNode
}

type candidateResult struct {
	index    int
	hits     []SearchHit
	scanned  int
	matched  int
	warnings []Warning
	err      error
	fatal    bool
	complete bool
}

type searchableField struct {
	name string
	text string
}

type textMatcher struct {
	pattern          string
	patternRunes     []rune
	patternASCII     string
	asciiSkip        [256]int
	foldsBeyondASCII bool
	regex            *regexp.Regexp
	caseSensitive    bool
}

func runSearch(ctx context.Context, args []string, help io.Writer, factory RegistryFactory) (any, string, error) {
	options, pattern, err := parseSearchOptions(args, help)
	if errorsIsHelp(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	matcher, err := newTextMatcher(pattern, options.regex, options.caseSensitive)
	if err != nil {
		return nil, "", err
	}
	registry, err := factory(ctx, options.common.registryOptions())
	if err != nil {
		return nil, "", runtimeError("internal", err.Error())
	}
	roots, diagnostics, err := registry.DiscoverWithDiagnostics(ctx)
	if err != nil {
		return nil, "", runtimeError("internal", err.Error())
	}
	roots, allNodes, commandDiags := addressableGraph(roots, commandDiagnostics(diagnostics))
	nodesBySession := make(map[*model.Session]graphNode, len(allNodes))
	for _, node := range allNodes {
		nodesBySession[node.session] = node
	}
	candidates, scoped, err := searchCandidates(roots, allNodes, nodesBySession, commandDiags, options)
	if err != nil {
		return nil, "", err
	}
	response, err := executeSearch(ctx, registry, candidates, matcher, options, scoped)
	if err != nil {
		return nil, "", err
	}
	if !scoped {
		response.Warnings = append(discoveryWarnings(commandDiags), response.Warnings...)
	}
	response.Warnings = nonNil(response.Warnings)
	if len(response.Warnings) > 0 {
		response.Page.Complete = false
		response.Page.Total = nil
	}
	if err := fitSearchResponse(&response); err != nil {
		return nil, "", err
	}
	return response, options.common.format, nil
}

func parseSearchOptions(args []string, help io.Writer) (searchOptions, string, error) {
	options := searchOptions{limit: 30, snippet: 200}
	flags := newFlagSet("agtlog search", help, searchUsage)
	addCommonFlags(flags, &options.common)
	flags.StringVar(&options.project, "project", "", "keep sessions whose project basename is this name")
	flags.StringVar(&options.cwd, "cwd", "", "keep sessions whose working directory is this path or below it")
	flags.StringVar(&options.since, "since", "", "keep sessions updated at or after this time: RFC 3339, YYYY-MM-DD, or a duration such as 7d")
	flags.StringVar(&options.until, "until", "", "keep sessions updated at or before this time: RFC 3339, YYYY-MM-DD, or a duration such as 7d")
	flags.StringVar(&options.kind, "kind", "", "keep only these comma-separated event kinds")
	flags.StringVar(&options.session, "session", "", "search only this selector's session and its descendants")
	flags.BoolVar(&options.regex, "regex", false, "interpret the pattern as an RE2 expression")
	flags.BoolVar(&options.caseSensitive, "case-sensitive", false, "match case exactly")
	flags.IntVar(&options.limit, "limit", 30, "maximum hits to return")
	flags.IntVar(&options.offset, "offset", 0, "ordered hits to skip")
	flags.BoolVar(&options.all, "all", false, "remove the hit count limit (the 256 KiB response limit still applies)")
	flags.IntVar(&options.snippet, "snippet", 200, "runes of context on each side of the first match")
	operands, err := parseFlexible(flags, args, "search pattern")
	if err != nil {
		return searchOptions{}, "", err
	}
	if err := options.common.validate(); err != nil {
		return searchOptions{}, "", err
	}
	if operands[0] == "" {
		return searchOptions{}, "", usageError("search pattern must not be empty")
	}
	if options.limit <= 0 {
		return searchOptions{}, "", usageError("--limit must be greater than zero")
	}
	if options.offset < 0 {
		return searchOptions{}, "", usageError("--offset must not be negative")
	}
	if options.snippet < 0 {
		return searchOptions{}, "", usageError("--snippet must not be negative")
	}
	window, err := parseUpdateWindow(options.since, options.until, time.Now(), time.Local)
	if err != nil {
		return searchOptions{}, "", err
	}
	options.window = window
	if _, err := parseKinds(options.kind); err != nil {
		return searchOptions{}, "", err
	}
	if _, err := newTextMatcher(operands[0], options.regex, options.caseSensitive); err != nil {
		return searchOptions{}, "", err
	}
	return options, operands[0], nil
}

func searchUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: agtlog search <pattern> [flags]")
	_, _ = fmt.Fprintln(output, "Searches cleaned session timelines and prints hits as indented JSON by default.")
	_, _ = fmt.Fprintln(output, "Flags can appear before or after <pattern>.")
	_, _ = fmt.Fprintln(output, "Put a pattern that begins with '-' after --.")
}

func searchCandidates(roots []*model.Session, allNodes []graphNode, nodesBySession map[*model.Session]graphNode, diagnostics []commandDiagnostic, options searchOptions) ([]searchCandidate, bool, error) {
	if options.session != "" {
		selected, err := resolveSelector(options.session, allNodes, diagnostics)
		if err != nil {
			return nil, true, err
		}
		if !searchSummaryMatches(selected.session, options) {
			return []searchCandidate{}, true, nil
		}
		if hasUnreadableDescendant(selected, diagnostics) {
			return nil, true, runtimeError("unreadable_session", "the selected session graph contains an unreadable descendant")
		}
		nodes := subtreeNodes(selected, nodesBySession)
		return []searchCandidate{{start: selected, nodes: nodes}}, true, nil
	}
	filtered := make([]graphNode, 0, len(roots))
	for _, root := range roots {
		if searchSummaryMatches(root, options) {
			if node, exists := nodesBySession[root]; exists {
				filtered = append(filtered, node)
			}
		}
	}
	slices.SortFunc(filtered, func(left, right graphNode) int {
		if !left.session.UpdatedAt.Equal(right.session.UpdatedAt) {
			return -left.session.UpdatedAt.Compare(right.session.UpdatedAt)
		}
		return strings.Compare(left.ref, right.ref)
	})
	candidates := make([]searchCandidate, 0, len(filtered))
	for index, root := range filtered {
		candidates = append(candidates, searchCandidate{index: index, start: root, nodes: subtreeNodes(root, nodesBySession)})
	}
	return candidates, false, nil
}

func hasUnreadableDescendant(selected graphNode, diagnostics []commandDiagnostic) bool {
	path := selected.session.Path
	if path == "" || strings.Contains(path, "#") {
		return false
	}
	// Claude stores the subagent transcripts of <session>.jsonl below <session>/.
	companion := strings.TrimSuffix(filepath.Clean(path), filepath.Ext(path))
	for _, diagnostic := range diagnostics {
		if diagnostic.code != "unreadable_session" || diagnostic.agent != selected.session.Agent {
			continue
		}
		relative, err := filepath.Rel(companion, filepath.Clean(diagnostic.path))
		if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func searchSummaryMatches(session *model.Session, options searchOptions) bool {
	if options.common.agent != "" && string(session.Agent) != options.common.agent || options.project != "" && session.Project != options.project || options.cwd != "" && !cwdContains(options.cwd, session.CWD) || !options.window.contains(session.UpdatedAt) {
		return false
	}
	return true
}

func subtreeNodes(start graphNode, nodesBySession map[*model.Session]graphNode) []graphNode {
	result := make([]graphNode, 0)
	var appendSession func(*model.Session)
	appendSession = func(session *model.Session) {
		node, exists := nodesBySession[session]
		if !exists {
			return
		}
		result = append(result, node)
		for _, child := range session.Subagents {
			appendSession(child)
		}
	}
	appendSession(start.session)
	slices.SortFunc(result, func(left, right graphNode) int { return strings.Compare(left.ref, right.ref) })
	return result
}

func executeSearch(ctx context.Context, registry Registry, candidates []searchCandidate, matcher textMatcher, options searchOptions, scoped bool) (SearchResponse, error) {
	response := SearchResponse{SchemaVersion: SchemaVersion, Command: "search", Hits: []SearchHit{}, Warnings: []Warning{}}
	if len(candidates) == 0 {
		total := 0
		response.Page = SearchPage{Offset: options.offset, Limit: searchPageLimit(options), Complete: true, Total: &total, NextOffset: options.offset}
		return response, nil
	}
	kinds, _ := parseKinds(options.kind)
	needed := searchScanLimit(options)
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan searchCandidate)
	results := make(chan candidateResult, min(len(candidates), max(1, runtime.GOMAXPROCS(0))))
	workers := min(len(candidates), max(1, runtime.GOMAXPROCS(0)))
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for candidate := range jobs {
				result := scanCandidate(scanCtx, registry, candidate, matcher, kinds, options.snippet, needed)
				select {
				case results <- result:
				case <-scanCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, candidate := range candidates {
			select {
			case jobs <- candidate:
			case <-scanCtx.Done():
				return
			}
		}
	}()
	go func() {
		group.Wait()
		close(results)
	}()

	// Workers finish out of order. Committing results in candidate order keeps
	// the hit order and the stopping point deterministic.
	pending := make(map[int]candidateResult)
	nextCandidate := 0
	orderedHits := make([]SearchHit, 0)
	exhaustive := true
	stopped := false
	for result := range results {
		if stopped {
			continue
		}
		pending[result.index] = result
		for {
			current, ok := pending[nextCandidate]
			if !ok {
				break
			}
			delete(pending, nextCandidate)
			nextCandidate++
			if current.fatal {
				cancel()
				for range results {
				}
				return SearchResponse{}, runtimeError("internal", current.err.Error())
			}
			if current.err != nil && scoped {
				cancel()
				for range results {
				}
				return SearchResponse{}, runtimeError("unreadable_session", "the selected session graph could not be read: "+current.err.Error())
			}
			response.Warnings = append(response.Warnings, current.warnings...)
			response.Page.SessionsScanned += current.scanned
			response.Page.SessionsMatched += current.matched
			orderedHits = append(orderedHits, current.hits...)
			exhaustive = exhaustive && current.complete
			if needed > 0 && len(orderedHits) >= needed {
				orderedHits = orderedHits[:needed]
				exhaustive = false
				stopped = true
				cancel()
				break
			}
			if nextCandidate == len(candidates) {
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return SearchResponse{}, runtimeError("internal", err.Error())
	}
	start := min(options.offset, len(orderedHits))
	end := len(orderedHits)
	if !options.all && options.limit < end-start {
		end = start + options.limit
	}
	response.Hits = nonNil(append([]SearchHit(nil), orderedHits[start:end]...))
	// An unreadable session lowers complete, not has_more: has_more promises a hit
	// at next_offset, and paging past the last readable hit would never end.
	hasMore := len(orderedHits) > end || stopped
	nextOffset := options.offset + len(response.Hits)
	response.Page.Offset = options.offset
	response.Page.Limit = searchPageLimit(options)
	response.Page.Returned = len(response.Hits)
	response.Page.HasMore = hasMore
	response.Page.NextOffset = nextOffset
	response.Page.Complete = exhaustive && nextCandidate == len(candidates)
	if response.Page.Complete {
		total := len(orderedHits)
		response.Page.Total = &total
	}
	return response, nil
}

func searchPageLimit(options searchOptions) int {
	if options.all {
		return 0
	}
	return options.limit
}

// searchScanLimit returns how many ordered hits to collect: the page plus one hit
// that proves has_more. --all and larger limits collect at most the hits that one
// response can hold. Zero means no limit.
func searchScanLimit(options searchOptions) int {
	limit := options.limit
	if options.all || limit > maximumSearchHitsWithinBudget() {
		limit = maximumSearchHitsWithinBudget()
	}
	maximumInt := int(^uint(0) >> 1)
	if options.offset > maximumInt-limit-1 {
		return 0
	}
	return options.offset + limit + 1
}

// maximumSearchHitsWithinBudget is an upper bound because every encoded hit is at
// least as large as an empty top-level hit.
func maximumSearchHitsWithinBudget() int {
	encoded, _ := json.MarshalIndent(SearchHit{}, "", "  ")
	return machineResponseBudgetBytes/len(encoded) + 1
}

func fitSearchResponse(response *SearchResponse) error {
	if searchResponseFits(*response) {
		return nil
	}
	hits := response.Hits
	response.Page.HasMore = true
	response.Page.Complete = false
	response.Page.Total = nil
	best := -1
	for low, high := 0, len(hits)-1; low <= high; {
		candidate := low + (high-low)/2
		response.Hits = hits[:candidate]
		response.Page.Returned = candidate
		response.Page.NextOffset = response.Page.Offset + candidate
		if searchResponseFits(*response) {
			best = candidate
			low = candidate + 1
		} else {
			high = candidate - 1
		}
	}
	if best < 0 {
		return runtimeError("internal", "search metadata exceeds the response budget")
	}
	if best == 0 {
		return runtimeError("internal", "search hit metadata exceeds the response budget")
	}
	response.Hits = hits[:best]
	response.Page.Returned = len(response.Hits)
	response.Page.NextOffset = response.Page.Offset + len(response.Hits)
	return nil
}

func searchResponseFits(response SearchResponse) bool {
	encoded, _ := json.MarshalIndent(response, "", "  ")
	return len(encoded)+1 <= machineResponseBudgetBytes
}

func scanCandidate(ctx context.Context, registry Registry, candidate searchCandidate, matcher textMatcher, kinds map[model.EventKind]bool, snippet, capHits int) candidateResult {
	result := candidateResult{index: candidate.index, complete: true}
	if releaser, ok := registry.(detailReleaser); ok {
		defer releaser.ReleaseDetail(candidate.start.session)
	}
	failedPaths := make(map[string]bool)
	loadedPaths := make(map[string]bool)
	for _, node := range candidate.nodes {
		path := physicalSessionPath(node.session.Path)
		if loadedPaths[path] {
			continue
		}
		loadedPaths[path] = true
		if err := loadNodeDetail(ctx, registry, node.session); err != nil {
			if ctx.Err() != nil {
				result.err = ctx.Err()
				result.complete = false
				return result
			}
			result.err = err
			result.complete = false
			failedPaths[path] = true
			result.warnings = append(result.warnings, Warning{Code: "unreadable_session", Message: fmt.Sprintf("could not read %s session: %v", node.session.Agent, err), Ref: node.ref})
			continue
		}
	}
	for _, node := range candidate.nodes {
		if failedPaths[physicalSessionPath(node.session.Path)] {
			continue
		}
		if err := ctx.Err(); err != nil {
			result.err = err
			result.complete = false
			return result
		}
		result.scanned++
		matchedSession := false
		for index, event := range node.session.Events {
			kindName, valid := wireEventKind(event.Kind)
			if !valid {
				result.err = fmt.Errorf("event kind %q has no schema v1 mapping", event.Kind)
				result.fatal = true
				result.complete = false
				return result
			}
			if kinds != nil && !kinds[event.Kind] {
				continue
			}
			for _, field := range eventSearchFields(event) {
				start, end, count, ok := matcher.find(field.text)
				if !ok {
					continue
				}
				matchedSession = true
				result.hits = append(result.hits, SearchHit{
					Session: HitSession{Ref: node.ref, Agent: string(node.session.Agent), Project: node.session.Project, Title: node.session.Title, UpdatedAt: timestamp(node.session.UpdatedAt)},
					Event:   HitEvent{Index: index, Timestamp: timestamp(event.Timestamp), Kind: kindName, Tool: event.ToolName},
					Field:   field.name,
					Range:   [2]int{start, end},
					Snippet: matchSnippet(field.text, start, end, snippet),
					Matches: count,
				})
				if capHits > 0 && len(result.hits) >= capHits {
					result.complete = false
					if matchedSession {
						result.matched++
					}
					return result
				}
			}
		}
		if matchedSession {
			result.matched++
		}
	}
	return result
}

// physicalSessionPath strips the #suffix that marks an inline child stored in
// its parent's file.
func physicalSessionPath(path string) string {
	path, _, _ = strings.Cut(path, "#")
	return path
}

func eventSearchFields(event model.Event) []searchableField {
	projection := projectEventText(event)
	fields := []searchableField{{name: fieldText, text: projection.text}}
	fields = append(fields,
		searchableField{name: fieldToolInput, text: projection.input},
		searchableField{name: fieldToolDiff, text: projection.diff},
		searchableField{name: fieldToolOutput, text: projection.output},
		searchableField{name: fieldToolSummary, text: projection.summary},
	)
	return fields
}

func newTextMatcher(pattern string, regex, caseSensitive bool) (textMatcher, error) {
	matcher := textMatcher{pattern: pattern, patternRunes: []rune(pattern), caseSensitive: caseSensitive}
	if !regex {
		if !caseSensitive && isASCII(pattern) {
			matcher.patternASCII = asciiLower(pattern)
			for index := range matcher.asciiSkip {
				matcher.asciiSkip[index] = len(matcher.patternASCII)
			}
			for index := 0; index+1 < len(matcher.patternASCII); index++ {
				matcher.asciiSkip[matcher.patternASCII[index]] = len(matcher.patternASCII) - index - 1
			}
			matcher.foldsBeyondASCII = strings.ContainsAny(matcher.patternASCII, "ks")
		}
		return matcher, nil
	}
	expression := pattern
	if !caseSensitive {
		expression = "(?i:" + pattern + ")"
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return textMatcher{}, usageError("invalid regular expression: " + err.Error())
	}
	matcher.regex = compiled
	return matcher, nil
}

func (matcher textMatcher) find(value string) (int, int, int, bool) {
	if value == "" {
		return 0, 0, 0, false
	}
	if matcher.regex != nil {
		first := matcher.regex.FindStringIndex(value)
		if first == nil {
			return 0, 0, 0, false
		}
		count := 0
		_ = matcher.regex.ReplaceAllStringFunc(value, func(match string) string {
			count++
			return match
		})
		return utf8.RuneCountInString(value[:first[0]]), utf8.RuneCountInString(value[:first[1]]), count, true
	}
	if matcher.caseSensitive {
		first := strings.Index(value, matcher.pattern)
		if first < 0 {
			return 0, 0, 0, false
		}
		return utf8.RuneCountInString(value[:first]), utf8.RuneCountInString(value[:first+len(matcher.pattern)]), strings.Count(value, matcher.pattern), true
	}
	if matcher.patternASCII != "" && (!matcher.foldsBeyondASCII || !containsFoldOutlier(value)) {
		first, count := matcher.findASCII(value)
		if first < 0 {
			return 0, 0, 0, false
		}
		start := utf8.RuneCountInString(value[:first])
		return start, start + len(matcher.patternRunes), count, true
	}
	width := len(matcher.patternRunes)
	window := make([]rune, width)
	first, count, runeCount, nextStart := -1, 0, 0, 0
	for _, current := range value {
		window[runeCount%width] = current
		runeCount++
		start := runeCount - width
		if start < nextStart || start < 0 {
			continue
		}
		matched := true
		for index, patternRune := range matcher.patternRunes {
			if !equalFoldRune(window[(start+index)%width], patternRune) {
				matched = false
				break
			}
		}
		if matched {
			if first < 0 {
				first = start
			}
			count++
			nextStart = runeCount
		}
	}
	if first < 0 {
		return 0, 0, 0, false
	}
	return first, first + width, count, true
}

// containsFoldOutlier reports whether value contains U+212A KELVIN SIGN or U+017F
// LATIN SMALL LETTER LONG S. They are the only non-ASCII runes that simple case
// folding maps to an ASCII letter (k and s), and the byte-wise ASCII matcher misses them.
func containsFoldOutlier(value string) bool {
	return strings.Contains(value, "\u212A") || strings.Contains(value, "\u017F")
}

func equalFoldRune(left, right rune) bool {
	if left == right {
		return true
	}
	for folded := unicode.SimpleFold(left); folded != left; folded = unicode.SimpleFold(folded) {
		if folded == right {
			return true
		}
	}
	return false
}

func (matcher textMatcher) findASCII(value string) (int, int) {
	width := len(matcher.patternASCII)
	first, count := -1, 0
	for index := 0; index+width <= len(value); {
		matched := true
		for offset := width - 1; offset >= 0; offset-- {
			if asciiLowerByte(value[index+offset]) != matcher.patternASCII[offset] {
				matched = false
				break
			}
		}
		if matched {
			if first < 0 {
				first = index
			}
			count++
			index += width
			continue
		}
		index += matcher.asciiSkip[asciiLowerByte(value[index+width-1])]
	}
	return first, count
}

func isASCII(value string) bool {
	for index := range len(value) {
		if value[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func asciiLower(value string) string {
	result := []byte(value)
	for index := range result {
		result[index] = asciiLowerByte(result[index])
	}
	return string(result)
}

func asciiLowerByte(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func matchSnippet(value string, start, end, contextRunes int) string {
	from := max(0, start-contextRunes)
	maximumInt := int(^uint(0) >> 1)
	to := maximumInt
	if contextRunes <= maximumInt-end {
		to = end + contextRunes
	}
	fromByte, toByte, runeCount := runeByteRange(value, from, to)
	to = min(to, runeCount)
	var snippet strings.Builder
	if from > 0 {
		snippet.WriteRune('…')
	}
	snippet.WriteString(value[fromByte:toByte])
	if to < runeCount {
		snippet.WriteRune('…')
	}
	return snippet.String()
}

func runeByteRange(value string, from, to int) (int, int, int) {
	fromByte, toByte := len(value), len(value)
	runeIndex := 0
	for byteIndex := range value {
		if runeIndex == from {
			fromByte = byteIndex
		}
		if runeIndex == to {
			toByte = byteIndex
		}
		runeIndex++
	}
	if from >= runeIndex {
		fromByte = len(value)
	}
	if to >= runeIndex {
		toByte = len(value)
	}
	return fromByte, toByte, runeIndex
}
