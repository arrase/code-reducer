package engine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var jsonFenceRegex = regexp.MustCompile("(?s)^\\x60{3,}(?:json)?\\s*(.*?)\\s*\\x60{3,}$")

// CleanJSONResponse extracts JSON content from surrounding markdown code fences.
func CleanJSONResponse(response string) string {
	trimmed := strings.TrimSpace(response)
	
	// Strip markdown code fences if present
	if matches := jsonFenceRegex.FindStringSubmatch(trimmed); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	
	// Sometimes models output text before or after the JSON block. Let's find the first '{' or '[' and the last '}' or ']'.
	firstBrace := strings.Index(trimmed, "{")
	firstBracket := strings.Index(trimmed, "[")
	
	startIdx := -1
	if firstBrace != -1 && (firstBracket == -1 || firstBrace < firstBracket) {
		startIdx = firstBrace
	} else if firstBracket != -1 {
		startIdx = firstBracket
	}
	
	if startIdx != -1 {
		lastBrace := strings.LastIndex(trimmed, "}")
		lastBracket := strings.LastIndex(trimmed, "]")
		
		endIdx := -1
		if lastBrace != -1 && (lastBracket == -1 || lastBrace > lastBracket) {
			endIdx = lastBrace
		} else if lastBracket != -1 {
			endIdx = lastBracket
		}
		
		if endIdx != -1 && endIdx > startIdx {
			return trimmed[startIdx : endIdx+1]
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
