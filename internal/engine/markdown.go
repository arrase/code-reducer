package engine

import (
	"regexp"
	"strings"
)

var markdownFenceRe = regexp.MustCompile("(?s)^\\x60{3,}(?:markdown|json)?\\s*(.*?)\\s*\\x60{3,}$")

// stripOuterMarkdownFence strips surrounding markdown or json code fences from input strings.
func stripOuterMarkdownFence(content string) string {
	trimmed := strings.TrimSpace(content)
	if matches := markdownFenceRe.FindStringSubmatch(trimmed); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return trimmed
}
