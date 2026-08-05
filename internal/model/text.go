package model

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
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

func IsHardNoise(value string) bool {
	return CleanTimelineText(value) == ""
}

func CleanTimelineText(value string) string {
	tags := []string{"system-reminder", "permission-preamble", "local-command-caveat"}
	lower := strings.ToLower(value)
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
		// A block that owned its whole line leaves that line's newline behind.
		// Dropping it keeps the removal from reading as a paragraph break.
		if indent := len(bytes.TrimRight(stripped, " \t")); (indent == 0 || stripped[indent-1] == '\n') && offset < len(value) && value[offset] == '\n' {
			stripped, offset = stripped[:indent], offset+1
		}
	}
	value = string(stripped)
	lines := strings.Split(value, "\n")
	cleaned := lines[:0]
	for _, line := range lines {
		// Keep indentation so code blocks and nested lists survive; only the
		// blank lines that separate paragraphs are normalized.
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if strings.EqualFold(strings.TrimSpace(line), "warmup") {
			continue
		}
		if line == "" && (len(cleaned) == 0 || cleaned[len(cleaned)-1] == "") {
			// Removing a noise block leaves the blank lines that surrounded it,
			// so collapse runs into the single blank line a paragraph break needs.
			continue
		}
		cleaned = append(cleaned, line)
	}
	for len(cleaned) > 0 && cleaned[len(cleaned)-1] == "" {
		cleaned = cleaned[:len(cleaned)-1]
	}
	return strings.Join(cleaned, "\n")
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

func ElideEncrypted(text string) string {
	const (
		prefix    = "gAAAA"
		minLength = 64
	)
	var elided strings.Builder
	searchFrom, writeFrom := 0, 0
	for searchFrom < len(text) {
		offset := strings.Index(text[searchFrom:], prefix)
		if offset < 0 {
			break
		}
		start := searchFrom + offset
		if start > 0 && encryptedTokenChar(text[start-1]) {
			searchFrom = start + len(prefix)
			continue
		}
		end := start + len(prefix)
		for end < len(text) && encryptedTokenChar(text[end]) {
			end++
		}
		for end < len(text) && text[end] == '=' {
			end++
		}
		if end-start < minLength {
			searchFrom = start + len(prefix)
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
