package model

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const maxTitleRunes = 96
const maxDetailRunes = 4096

func CleanTitle(value string) string {
	for len(value) > 0 {
		line, rest, found := strings.Cut(value, "\n")
		if !found {
			rest = ""
		}
		if title := cleanTitleLine(line); title != "" {
			return title
		}
		value = rest
	}
	return ""
}

func CleanUniqueTitle(value string, shared map[string]bool) string {
	fallback := CleanTitle(value)
	for len(value) > 0 {
		line, rest, found := strings.Cut(value, "\n")
		if !found {
			rest = ""
		}
		if title := cleanTitleLine(line); title != "" && !shared[title] {
			return title
		}
		value = rest
	}
	return fallback
}

func cleanTitleLine(line string) string {
	line = strings.TrimSpace(line)
	for strings.HasPrefix(line, "<") {
		end := strings.IndexByte(line, '>')
		if end < 0 {
			break
		}
		line = line[end+1:]
		for len(line) > 0 {
			r, size := utf8.DecodeRuneInString(line)
			if !unicode.IsSpace(r) {
				break
			}
			line = line[size:]
		}
	}
	runes := make([]rune, 0, maxTitleRunes)
	started, truncated := false, false
	for index := 0; index < len(line); {
		if strings.HasPrefix(line[index:], "</") {
			if end := strings.IndexByte(line[index:], '>'); end >= 0 {
				index += end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(line[index:])
		index += size
		if !started && (unicode.IsSpace(r) || strings.ContainsRune("#>*-", r)) {
			continue
		}
		started = true
		if len(runes) == maxTitleRunes {
			truncated = true
			break
		}
		runes = append(runes, r)
	}
	result := strings.TrimSpace(string(runes))
	if truncated && len(runes) == maxTitleRunes {
		return string(runes[:maxTitleRunes-1]) + "…"
	}
	return result
}

func CleanTimelineText(value string) string {
	tags := []string{"system-reminder", "permission-preamble", "local-command-caveat"}
	lower := asciiLower(value)
	stripped := make([]byte, 0, len(value))
	for offset := 0; offset < len(value); {
		start, matchedTag := len(value), ""
		for _, tag := range tags {
			if relative := strings.Index(lower[offset:], "<"+tag); relative >= 0 && offset+relative < start {
				start, matchedTag = offset+relative, tag
			}
		}
		if matchedTag == "" {
			stripped = append(stripped, value[offset:]...)
			break
		}
		stripped = append(stripped, value[offset:start]...)
		closeTag := "</" + matchedTag + ">"
		end := strings.Index(lower[start:], closeTag)
		if end < 0 {
			break
		}
		offset = start + end + len(closeTag)
		// If the block filled its whole line, drop the newline after it too.
		// Otherwise the removal reads as a paragraph break.
		if indent := len(bytes.TrimRight(stripped, " \t")); (indent == 0 || stripped[indent-1] == '\n') && offset < len(value) && value[offset] == '\n' {
			stripped, offset = stripped[:indent], offset+1
		}
	}
	value = string(stripped)
	lines := strings.Split(value, "\n")
	cleaned := lines[:0]
	for _, line := range lines {
		// Trim only trailing space. Code blocks and nested lists need their
		// indentation.
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if strings.EqualFold(strings.TrimSpace(line), "warmup") {
			continue
		}
		if line == "" && (len(cleaned) == 0 || cleaned[len(cleaned)-1] == "") {
			// A removed noise block leaves the blank lines around it. Keep one
			// blank line per run as the paragraph break.
			continue
		}
		cleaned = append(cleaned, line)
	}
	for len(cleaned) > 0 && cleaned[len(cleaned)-1] == "" {
		cleaned = cleaned[:len(cleaned)-1]
	}
	return strings.Join(cleaned, "\n")
}

// asciiLower folds only ASCII letters, so a byte offset in the result is the
// same offset in s. strings.ToLower changes the UTF-8 length of letters such
// as "İ" and "Ⱥ".
func asciiLower(s string) string {
	folded := []byte(s)
	for index, char := range folded {
		if 'A' <= char && char <= 'Z' {
			folded[index] = char + ('a' - 'A')
		}
	}
	return string(folded)
}

func BoundedDetailText(value string, limits ...int) string {
	limit := maxDetailRunes
	if len(limits) > 0 {
		limit = limits[0]
	}
	if limit <= 0 {
		return value
	}
	runeCount := utf8.RuneCountInString(value)
	if runeCount <= limit {
		return value
	}
	half := (limit - 1) / 2
	tailRunes := limit - 1 - half
	headByte, tailByte := len(value), len(value)
	seen := 0
	for index := range value {
		if seen == half {
			headByte = index
		}
		if seen == runeCount-tailRunes {
			tailByte = index
			break
		}
		seen++
	}
	return value[:headByte] + "…" + value[tailByte:]
}

// TerminalText makes log text safe to print. It strips ANSI escape sequences
// first, because a sequence whose ESC became a space prints its tail, for
// example "[31m", as text. Then every other control or Unicode format (Cf)
// character becomes a space, so the text cannot move the cursor or reorder
// bidirectional text.
func TerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

// TerminalLine returns TerminalText of the first maxRunes runes of value, with
// each whitespace run collapsed to one space. The cap applies to the input, so
// the cost stays bounded for event text of any length. A sequence cut at the
// cap leaves no residue, because ansi.Strip drops an unterminated sequence. A
// maxRunes of zero or less sets no cap.
func TerminalLine(value string, maxRunes int) string {
	if maxRunes > 0 {
		for index := range value {
			if maxRunes == 0 {
				value = value[:index]
				break
			}
			maxRunes--
		}
	}
	return strings.Join(strings.Fields(TerminalText(value)), " ")
}

// SessionFilterText returns the text that `list --query` and the terminal UI
// filter match, so that one query selects the same sessions in both. It is the
// agent, project, and title as the terminal shows them.
func SessionFilterText(session *Session) string {
	return strings.ToLower(strings.Join([]string{
		TerminalLine(string(session.Agent), 0), TerminalLine(session.Project, 0), TerminalLine(session.Title, 0),
	}, " "))
}

// SessionFilterQuery normalizes a query for matching against SessionFilterText.
func SessionFilterQuery(query string) string {
	return strings.ToLower(TerminalLine(query, 0))
}

func ElideEncrypted(text string) string {
	const (
		fernetPrefix = "gAAAA"
		minLength    = 64
	)
	var elided strings.Builder
	searchFrom, writeFrom := 0, 0
	for searchFrom < len(text) {
		offset := strings.Index(text[searchFrom:], fernetPrefix)
		if offset < 0 {
			break
		}
		start := searchFrom + offset
		if start > 0 && encryptedTokenChar(text[start-1]) {
			searchFrom = start + len(fernetPrefix)
			continue
		}
		end := start + len(fernetPrefix)
		for end < len(text) && encryptedTokenChar(text[end]) {
			end++
		}
		for end < len(text) && text[end] == '=' {
			end++
		}
		if end-start < minLength {
			searchFrom = start + len(fernetPrefix)
			continue
		}
		if elided.Len() == 0 {
			elided.Grow(len(text))
		}
		elided.WriteString(text[writeFrom:start])
		fmt.Fprintf(&elided, "<encrypted %d chars>", end-start)
		writeFrom, searchFrom = end, end
	}
	if elided.Len() == 0 {
		return text
	}
	elided.WriteString(text[writeFrom:])
	return elided.String()
}

func encryptedTokenChar(char byte) bool {
	return char >= 'A' && char <= 'Z' ||
		char >= 'a' && char <= 'z' ||
		char >= '0' && char <= '9' ||
		char == '_' || char == '-'
}
