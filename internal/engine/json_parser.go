package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var markdownFenceRe = regexp.MustCompile("(?s)^\\x60{3,}(?:markdown|json)?\\s*(.*?)\\s*\\x60{3,}$")

// StripOuterMarkdownFence strips surrounding markdown or json code fences from input strings.
func StripOuterMarkdownFence(content string) string {
	trimmed := strings.TrimSpace(content)
	if matches := markdownFenceRe.FindStringSubmatch(trimmed); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return trimmed
}

// CleanJSONResponse extracts JSON content from surrounding markdown code fences.
func CleanJSONResponse(response string) string {
	trimmed := StripOuterMarkdownFence(response)

	firstBrace := strings.Index(trimmed, "{")
	firstBracket := strings.Index(trimmed, "[")

	startIdx := -1
	if firstBrace != -1 && (firstBracket == -1 || firstBrace < firstBracket) {
		startIdx = firstBrace
	} else if firstBracket != -1 {
		startIdx = firstBracket
	}

	if startIdx != -1 {
		var stack []rune
		inString := false
		var escape bool

		for i, r := range trimmed[startIdx:] {
			if escape {
				escape = false
				continue
			}
			if r == '\\' && inString {
				escape = true
				continue
			}
			if r == '"' {
				inString = !inString
				continue
			}

			if !inString {
				if r == '{' || r == '[' {
					stack = append(stack, r)
				} else if r == '}' || r == ']' {
					if len(stack) > 0 {
						last := stack[len(stack)-1]
						if (r == '}' && last == '{') || (r == ']' && last == '[') {
							stack = stack[:len(stack)-1]
						}
					}
					if len(stack) == 0 {
						return trimmed[startIdx : startIdx+i+1]
					}
				}
			}
		}
	}

	return trimmed
}

// UnmarshalJSONResponse cleans the raw response and unmarshals it into target.
func UnmarshalJSONResponse(rawResponse string, target interface{}) error {
	cleaned := CleanJSONResponse(rawResponse)
	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		return fmt.Errorf("failed to unmarshal JSON (cleaned: %q): %w", cleaned, err)
	}
	return nil
}
