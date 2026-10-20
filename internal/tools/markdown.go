package tools

import (
	"fmt"
	"strings"
	"unicode"
)

// ParseFenceOpen detects a Markdown fenced code block opener (``` or ~~~) and returns
// the fence character, fence length, info string, and whether it is a valid opener.
func ParseFenceOpen(line string) (char byte, count int, info string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) < 3 {
		return 0, 0, "", false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0, "", false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	info = strings.TrimSpace(trimmed[n:])
	if ch == '`' && strings.ContainsRune(info, '`') {
		return 0, 0, "", false
	}
	return ch, n, info, true
}

// IsFenceClose checks whether line closes an open fenced code block of fenceChar and fenceCount.
func IsFenceClose(line string, fenceChar byte, fenceCount int) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < fenceCount {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != fenceChar {
			return false
		}
	}
	return true
}

// ParseATXHeading parses an ATX Markdown heading (# through ######) and returns its level and title.
func ParseATXHeading(line string) (level int, title string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) == 0 || trimmed[0] != '#' {
		return 0, "", false
	}
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level > 6 {
		return 0, "", false
	}
	rest := trimmed[level:]
	if len(rest) > 0 && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	title = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), "#"))
	return level, title, true
}

// SlugifyHeading converts a Markdown heading title into a GitHub-style anchor slug.
func SlugifyHeading(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	var b strings.Builder
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// NextHeadingAnchor computes the GitHub-style heading anchor for title, appending -1, -2, etc.
// for duplicate heading slugs within the same document tracked in seenCounts.
func NextHeadingAnchor(title string, seenCounts map[string]int) string {
	slug := SlugifyHeading(title)
	if slug == "" {
		return ""
	}
	count := seenCounts[slug]
	seenCounts[slug] = count + 1
	if count == 0 {
		return slug
	}
	return fmt.Sprintf("%s-%d", slug, count)
}

// TruncateRunes truncates s to at most maxRunes runes, appending "..." when truncated.
func TruncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return strings.TrimRightFunc(string(runes[:maxRunes-3]), unicode.IsSpace) + "..."
}
