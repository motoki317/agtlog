package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
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

func BoundedDetailText(value string) string {
	runeCount := utf8.RuneCountInString(value)
	if runeCount <= maxDetailRunes {
		return value
	}
	half := (maxDetailRunes - 1) / 2
	tailRunes := maxDetailRunes - 1 - half
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

func BoundedRawRecord(value string) string {
	safe := ElideEncrypted(value)
	if utf8.RuneCountInString(safe) <= maxDetailRunes {
		return safe
	}
	decoder := json.NewDecoder(strings.NewReader(safe))
	decoder.UseNumber()
	var decoded any
	if decoder.Decode(&decoded) != nil {
		return BoundedDetailText(safe)
	}
	budget := maxDetailRunes - 512
	bounded := boundJSONValue(decoded, &budget, 0)
	if encoded, ok := encodeJSON(bounded); ok && utf8.RuneCountInString(encoded) <= maxDetailRunes {
		return encoded
	}
	encoded, _ := encodeJSON(map[string]string{"_agtlog_bounded_raw": boundJSONText(safe, maxDetailRunes/8)})
	return encoded
}

func boundJSONValue(value any, budget *int, depth int) any {
	const (
		maxDepth        = 12
		maxObjectFields = 32
		maxArrayItems   = 16
		maxKeyRunes     = 64
		maxStringRunes  = 512
	)
	if depth >= maxDepth {
		return "<bounded nested value>"
	}
	switch value := value.(type) {
	case string:
		limit := min(maxStringRunes, max(1, *budget/6))
		bounded := boundJSONText(value, limit)
		*budget -= jsonEncodedSize(bounded)
		return bounded
	case []any:
		keep := min(len(value), maxArrayItems)
		head, tail := keep/2, keep-keep/2
		boundedHead := make([]any, 0, head)
		for index := 0; index < head && *budget > 128; index++ {
			boundedHead = append(boundedHead, boundJSONValue(value[index], budget, depth+1))
		}
		boundedTail := make([]any, 0, tail)
		for index := len(value) - tail; index < len(value) && *budget > 128; index++ {
			boundedTail = append(boundedTail, boundJSONValue(value[index], budget, depth+1))
		}
		bounded := make([]any, 0, len(boundedHead)+len(boundedTail)+1)
		bounded = append(bounded, boundedHead...)
		if omitted := len(value) - len(boundedHead) - len(boundedTail); omitted > 0 {
			bounded = append(bounded, map[string]int{"_agtlog_omitted_items": omitted})
		}
		bounded = append(bounded, boundedTail...)
		return bounded
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		bounded := make(map[string]any, min(len(keys), maxObjectFields)+1)
		kept := 0
		for _, key := range keys {
			if kept >= maxObjectFields || *budget <= 128 {
				break
			}
			boundedKey := uniqueBoundedJSONKey(key, bounded, maxKeyRunes, kept+1)
			*budget -= jsonEncodedSize(boundedKey) + 2
			bounded[boundedKey] = boundJSONValue(value[key], budget, depth+1)
			kept++
		}
		if kept < len(keys) {
			marker := uniqueBoundedJSONKey("_agtlog_omitted_fields", bounded, maxKeyRunes, kept+1)
			bounded[marker] = len(keys) - kept
		}
		return bounded
	default:
		*budget -= jsonEncodedSize(value)
		return value
	}
}

func uniqueBoundedJSONKey(key string, existing map[string]any, maxRunes, sequence int) string {
	safe := ElideEncrypted(key)
	candidate := safe
	if utf8.RuneCountInString(candidate) > maxRunes {
		suffix := fmt.Sprintf("#%d", sequence)
		candidate = boundJSONText(safe, maxRunes-utf8.RuneCountInString(suffix)) + suffix
	}
	if _, found := existing[candidate]; !found {
		return candidate
	}
	for collision := 1; ; collision++ {
		suffix := fmt.Sprintf("#%d-%d", sequence, collision)
		candidate = boundJSONText(safe, max(1, maxRunes-utf8.RuneCountInString(suffix))) + suffix
		if _, found := existing[candidate]; !found {
			return candidate
		}
	}
}

func jsonEncodedSize(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 32
	}
	return len(encoded)
}

func boundJSONText(value string, limit int) string {
	value = ElideEncrypted(value)
	value = BoundedDetailText(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return "…"
	}
	head := (limit - 1) / 2
	return string(runes[:head]) + "…" + string(runes[len(runes)-(limit-1-head):])
}

func encodeJSON(value any) (string, bool) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return "", false
	}
	return strings.TrimSuffix(encoded.String(), "\n"), true
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
