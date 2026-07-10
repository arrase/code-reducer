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

func extractBalancedJSON(input string) (string, error) {
	var stack []rune
	inString := false
	var escape bool

	for i, r := range input {
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
				} else {
					return "", fmt.Errorf("unbalanced closing delimiter")
				}
				if len(stack) == 0 {
					return input[:i+len(string(r))], nil
				}
			}
		}
	}
	return "", fmt.Errorf("unbalanced delimiters")
}

// CleanJSONResponse is kept for compatibility and wraps around extractBalancedJSON.
func CleanJSONResponse(response string) string {
	trimmed := StripOuterMarkdownFence(response)
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '{' || trimmed[i] == '[' {
			if candidate, err := extractBalancedJSON(trimmed[i:]); err == nil {
				return candidate
			}
		}
	}
	return trimmed
}

// UnmarshalJSONResponse cleans the raw response and unmarshals it into target.
func UnmarshalJSONResponse(rawResponse string, target interface{}) error {
	trimmed := StripOuterMarkdownFence(rawResponse)
	
	// Collect all balanced JSON candidates starting with { or [
	var candidates []string
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '{' || trimmed[i] == '[' {
			if candidate, err := extractBalancedJSON(trimmed[i:]); err == nil {
				candidates = append(candidates, candidate)
			}
		}
	}

	var lastErr error
	for _, candidate := range candidates {
		if err := json.Unmarshal([]byte(candidate), target); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	// Final fallback: try to unmarshal the whole trimmed string
	if err := json.Unmarshal([]byte(trimmed), target); err == nil {
		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("failed to unmarshal JSON candidates: %w", lastErr)
	}
	return fmt.Errorf("no valid JSON found in response")
}
