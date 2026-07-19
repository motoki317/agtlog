package model

import (
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
	var output strings.Builder
	output.Grow(len(value))
	for offset := 0; offset < len(value); {
		start, matchedTag := len(value), ""
		for _, tag := range tags {
			if relative := strings.Index(lower[offset:], "<"+tag); relative >= 0 && offset+relative < start {
				start, matchedTag = offset+relative, tag
			}
		}
		if matchedTag == "" {
			output.WriteString(value[offset:])
			break
		}
		output.WriteString(value[offset:start])
		closeTag := "</" + matchedTag + ">"
		end := strings.Index(lower[start:], closeTag)
		if end < 0 {
			break
		}
		offset = start + end + len(closeTag)
	}
	value = output.String()
	lines := strings.Split(value, "\n")
	cleaned := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "warmup") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}

func BoundedDetailText(value string) string {
	runes := []rune(value)
	if len(runes) <= maxDetailRunes {
		return value
	}
	half := (maxDetailRunes - 1) / 2
	return string(runes[:half]) + "…" + string(runes[len(runes)-(maxDetailRunes-1-half):])
}
