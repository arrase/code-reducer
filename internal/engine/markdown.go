package engine

import (
	"regexp"
	"strings"
)

var (
	markdownFenceRe   = regexp.MustCompile("(?s)^\\x60{3,}(?:markdown|json)?\\s*(.*?)\\s*\\x60{3,}$")
	fenceLineRe       = regexp.MustCompile("^\\s*(?:\\x60{3,}|~{3,})")
	headingLineRe     = regexp.MustCompile("^\\s{0,3}#{1,6}\\s")
	bulletPrefixRe    = regexp.MustCompile(`^(?:[-*+•]|\d+[.)])\s+`)
	collapsedSpaceRe  = regexp.MustCompile(`\s+`)
	trailingPunctTrim = " .:;,-"
	sentenceEndRunes  = ".!?"
	clauseEndRunes    = ":;,"
)

// stripOuterMarkdownFence strips surrounding markdown or json code fences from input strings.
func stripOuterMarkdownFence(content string) string {
	trimmed := strings.TrimSpace(content)
	if matches := markdownFenceRe.FindStringSubmatch(trimmed); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return trimmed
}

// stripSlotNoise removes the markdown a model reaches for even when asked for a
// bare line: fence lines, headings and list markers. A heading or fence line means
// the model is emitting structure instead of answering, so it is dropped outright.
func stripSlotNoise(content string) string {
	kept := make([]string, 0, strings.Count(content, "\n")+1)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if fenceLineRe.MatchString(line) || headingLineRe.MatchString(line) {
			continue
		}
		kept = append(kept, bulletPrefixRe.ReplaceAllString(line, ""))
	}
	return strings.Join(kept, "\n")
}

// sanitizeSlotLine normalizes a slot answer into exactly one clean line. A model
// asked for a single line still returns bullets, headings, fences, an enumeration
// of everything in a file or a rambling paragraph, and none of that may reach the
// page. The shape of the line is therefore decided here rather than trusted to the
// model: the first meaningful line is taken whole when the model bulleted it and
// only up to its first sentence otherwise, the result is bounded to
// slotLineWordBudget words, and every remaining line is dropped. Dropping the tail
// is intentional: the heading and the structure around the line are already fixed,
// so anything after the first unit is duplication. Trailing sentence punctuation is
// trimmed so the line reads as a fragment under its heading.
func sanitizeSlotLine(content string) string {
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || fenceLineRe.MatchString(line) || headingLineRe.MatchString(line) {
			continue
		}
		bulleted := bulletPrefixRe.MatchString(line)
		line = bulletPrefixRe.ReplaceAllString(line, "")
		if !bulleted {
			line = firstSentence(line)
		}
		return boundLineLength(strings.TrimSpace(line))
	}
	return ""
}

// boundLineLength cuts a line to slotLineWordBudget words. A cut lands on the last
// sentence or clause boundary at or before the budget, and on a word boundary when
// the text offers none, so a word is never split.
func boundLineLength(line string) string {
	words := strings.Fields(line)
	if len(words) <= slotLineWordBudget {
		return strings.Trim(strings.Join(words, " "), trailingPunctTrim)
	}
	bounded := strings.Join(words[:slotLineWordBudget], " ")
	if boundary := lastBoundary(bounded); boundary > 0 {
		return strings.Trim(bounded[:boundary], trailingPunctTrim)
	}
	return strings.Trim(bounded, trailingPunctTrim)
}

// firstCompleteSentence returns the leading sentence of text including its
// terminator, or an empty string when the text holds none. It is the salvage path
// for an answer that stopped on the generation limit: a complete sentence is a
// valid instance of a one-line contract, a clipped fragment is not.
func firstCompleteSentence(text string) string {
	trimmed := strings.TrimSpace(text)
	if end := firstBoundary(trimmed, sentenceEndRunes); end > 0 {
		return trimmed[:end]
	}
	return ""
}

// lastCompleteSentence returns text up to and including its last complete
// sentence, or an empty string when the text holds none. It is the salvage path
// for a clipped paragraph: the whole prefix is still a well-formed paragraph, so
// only the trailing fragment is dropped.
func lastCompleteSentence(text string) string {
	trimmed := strings.TrimSpace(text)
	if end := lastBoundaryOf(trimmed, sentenceEndRunes); end > 0 {
		return trimmed[:end]
	}
	return ""
}

// firstSentence returns the leading sentence of text, or the whole text when it
// holds no terminator.
func firstSentence(text string) string {
	if end := firstBoundary(text, sentenceEndRunes); end > 0 {
		return text[:end]
	}
	return text
}

// firstBoundary returns the index just past the first terminator of text that ends
// a sentence or clause there, or 0 when there is none.
func firstBoundary(text string, terminators string) int {
	for i := 0; i < len(text); i++ {
		if strings.IndexByte(terminators, text[i]) >= 0 && isBoundaryAt(text, i) {
			return i + 1
		}
	}
	return 0
}

// lastBoundary returns the index just past the last sentence or clause boundary of
// text, or 0 when it has none.
func lastBoundary(text string) int {
	return lastBoundaryOf(text, sentenceEndRunes+clauseEndRunes)
}

// lastBoundaryOf returns the index just past the last terminator of text that ends
// a unit there, or 0 when it has none.
func lastBoundaryOf(text, terminators string) int {
	best := 0
	for i := 0; i < len(text); i++ {
		if strings.IndexByte(terminators, text[i]) >= 0 && isBoundaryAt(text, i) {
			best = i + 1
		}
	}
	return best
}

// isBoundaryAt reports whether the terminator at index i really ends a unit there.
// It must be followed by whitespace or the end of the text, and a period that closes
// a single letter is an abbreviation such as "e.g." rather than a sentence end.
func isBoundaryAt(text string, i int) bool {
	if i+1 < len(text) {
		if !strings.ContainsRune(" \t\n", rune(text[i+1])) {
			return false
		}
	}
	if text[i] == '.' {
		return !isAbbreviationPeriod(text, i)
	}
	return true
}

// isAbbreviationPeriod reports whether the period at index i closes a single
// letter, as in "e.g." or "i.e.", rather than a sentence.
func isAbbreviationPeriod(text string, i int) bool {
	return i > 0 && isLetter(rune(text[i-1])) && (i < 2 || !isLetter(rune(text[i-2])))
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// sanitizeSlotParagraph normalizes a slot answer into exactly one clean paragraph.
func sanitizeSlotParagraph(content string) string {
	return collapsedSpaceRe.ReplaceAllString(stripSlotNoise(content), " ")
}
