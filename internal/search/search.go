package search

import (
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/arrase/code-reducer/internal/security"
	"github.com/arrase/code-reducer/internal/tools"
)

const (
	bm25K1            = 1.2
	bm25B             = 0.75
	defaultLimit      = 10
	maxSnippetRunes   = 160
	titleWeight       = 3.0
	sourceWeight      = 2.0
	bodyWeight        = 1.0
	pathHintBoost     = 2.0
	provenancePrefix  = "> **Module:**"
	sourceFilesMarker = "**Source Files:**"
)

// SearchResult represents a ranked documentation section returned by SearchWiki.
type SearchResult struct {
	Page    string
	Anchor  string
	Target  string
	Heading string
	Snippet string
	Sources []string
	Score   float64
}

type wikiSection struct {
	page        string
	anchor      string
	target      string
	heading     string
	body        string
	modPath     string
	sources     []string
	titleTerms  map[string]int
	sourceTerms map[string]int
	bodyTerms   map[string]int
	docLen      float64
}

// SearchWiki searches all markdown documentation under docsDir and returns the top-ranked sections using BM25.
func SearchWiki(repoRoot, docsDir, query string, pathHints []string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	docsAbs, err := security.SafeResolve(repoRoot, docsDir)
	if err != nil {
		return nil, err
	}

	queryTokens := uniqueTokens(tokenize(query))
	if len(queryTokens) == 0 {
		return nil, nil
	}

	mdFiles, err := discoverWikiMarkdownFiles(docsAbs)
	if err != nil {
		return nil, err
	}

	var sections []wikiSection
	for _, pageRel := range mdFiles {
		relToRoot := filepath.Join(docsDir, filepath.FromSlash(pageRel))
		data, err := tools.ReadFileSafely(repoRoot, relToRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", pageRel, err)
		}
		sections = append(sections, parseMarkdownSections(pageRel, string(data))...)
	}

	if len(sections) == 0 {
		return nil, nil
	}

	return scoreAndRankSections(sections, queryTokens, pathHints, limit), nil
}

// ReadWikiSection reads a full markdown page or a specific heading section (target format: "page.md" or "page.md#anchor").
func ReadWikiSection(repoRoot, docsDir, target string) (string, error) {
	docsAbs, err := security.SafeResolve(repoRoot, docsDir)
	if err != nil {
		return "", err
	}

	cleanedTarget := strings.TrimSpace(target)
	if cleanedTarget == "" {
		return "", fmt.Errorf("empty read target")
	}

	pagePart, anchor, hasAnchor := strings.Cut(cleanedTarget, "#")
	pageSlash := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(pagePart)), "./")
	docsSlash := strings.Trim(filepath.ToSlash(filepath.Clean(docsDir)), "/")
	if docsSlash != "" && docsSlash != "." && strings.HasPrefix(pageSlash, docsSlash+"/") {
		pageSlash = strings.TrimPrefix(pageSlash, docsSlash+"/")
	}
	if pageSlash == "" || strings.HasPrefix(pageSlash, "/") {
		return "", fmt.Errorf("%w: %q", security.ErrPathTraversal, target)
	}

	relToRoot := filepath.Join(docsDir, filepath.FromSlash(pageSlash))
	targetAbs, err := security.SafeResolve(repoRoot, relToRoot)
	if err != nil {
		return "", err
	}
	relToDocs, err := filepath.Rel(docsAbs, targetAbs)
	if err != nil || relToDocs == ".." || strings.HasPrefix(relToDocs, fmt.Sprintf("..%c", filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", security.ErrPathTraversal, target)
	}

	cleanRel := filepath.ToSlash(relToDocs)
	for _, seg := range strings.Split(cleanRel, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("invalid wiki target %q: hidden files and directories are not readable", target)
		}
	}
	if !strings.EqualFold(path.Ext(cleanRel), ".md") {
		return "", fmt.Errorf("invalid wiki target %q: only markdown (.md) files are readable", target)
	}

	data, err := tools.ReadFileSafely(repoRoot, relToRoot)
	if err != nil {
		return "", err
	}
	content := string(data)

	anchor = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(anchor)), "#")
	if !hasAnchor || anchor == "" {
		return content, nil
	}

	return extractSectionByAnchor(cleanRel, content, anchor)
}

func discoverWikiMarkdownFiles(docsAbs string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(docsAbs, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == docsAbs {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(name), ".md") || strings.EqualFold(name, "index.md") {
			return nil
		}
		rel, err := filepath.Rel(docsAbs, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func parseProvenance(markdown, pageRel string) (string, []string) {
	var modPath string
	var sources []string

	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, provenancePrefix) {
			continue
		}
		modPart, srcPart, hasSrc := strings.Cut(trimmed, "|")
		if ticks := extractBacktickValues(modPart); len(ticks) > 0 {
			modPath = ticks[0]
		}
		if hasSrc && strings.Contains(srcPart, sourceFilesMarker) {
			sources = extractBacktickValues(srcPart)
		}
		break
	}

	if modPath == "" && strings.HasPrefix(pageRel, "modules/") && strings.HasSuffix(pageRel, "/README.md") {
		inferred := strings.TrimSuffix(strings.TrimPrefix(pageRel, "modules/"), "/README.md")
		if inferred == "" {
			modPath = "."
		} else {
			modPath = inferred
		}
	} else if modPath == "" && pageRel == "modules/README.md" {
		modPath = "."
	}

	return modPath, sources
}

func extractBacktickValues(s string) []string {
	var vals []string
	for {
		start := strings.IndexByte(s, '`')
		if start == -1 {
			break
		}
		rest := s[start+1:]
		end := strings.IndexByte(rest, '`')
		if end == -1 {
			break
		}
		if val := strings.TrimSpace(rest[:end]); val != "" {
			vals = append(vals, val)
		}
		s = rest[end+1:]
	}
	return vals
}

func detectSplitHeadingLevel(lines []string) int {
	inFence := false
	var fenceChar byte
	var fenceCount int
	for _, line := range lines {
		if !inFence {
			if ch, count, _, ok := tools.ParseFenceOpen(line); ok {
				inFence = true
				fenceChar = ch
				fenceCount = count
				continue
			}
		} else {
			if tools.IsFenceClose(line, fenceChar, fenceCount) {
				inFence = false
			}
			continue
		}
		if level, _, ok := tools.ParseATXHeading(line); ok && level == 2 {
			return 2
		}
	}
	return 3
}

func parseMarkdownSections(pageRel, markdown string) []wikiSection {
	modPath, sources := parseProvenance(markdown, pageRel)
	lines := strings.Split(markdown, "\n")
	splitLevel := detectSplitHeadingLevel(lines)

	seenCounts := make(map[string]int)
	inFence := false
	var fenceChar byte
	var fenceCount int

	curHeading := strings.TrimSuffix(path.Base(pageRel), ".md")
	curAnchor := ""
	hasH1 := false
	var curBody []string
	var rawSections []wikiSection

	flushSection := func() {
		body := strings.TrimSpace(strings.Join(curBody, "\n"))
		if curAnchor == "" && body == "" {
			return
		}
		target := pageRel
		if curAnchor != "" {
			target = pageRel + "#" + curAnchor
		}
		rawSections = append(rawSections, buildWikiSection(pageRel, curAnchor, target, curHeading, body, modPath, sources))
	}

	for _, line := range lines {
		if !inFence {
			if ch, count, _, ok := tools.ParseFenceOpen(line); ok {
				inFence = true
				fenceChar = ch
				fenceCount = count
				curBody = append(curBody, line)
				continue
			}
		} else {
			if tools.IsFenceClose(line, fenceChar, fenceCount) {
				inFence = false
			}
			curBody = append(curBody, line)
			continue
		}

		level, headingText, ok := tools.ParseATXHeading(line)
		if !ok {
			if strings.HasPrefix(strings.TrimSpace(line), provenancePrefix) {
				continue
			}
			curBody = append(curBody, line)
			continue
		}

		anchor := tools.NextHeadingAnchor(headingText, seenCounts)
		if level == 1 && !hasH1 && curAnchor == "" && len(rawSections) == 0 && strings.TrimSpace(strings.Join(curBody, "\n")) == "" {
			hasH1 = true
			curHeading = headingText
			continue
		}
		if level == 1 || level == splitLevel {
			flushSection()
			curHeading = headingText
			curAnchor = anchor
			curBody = nil
			continue
		}

		curBody = append(curBody, line)
	}
	flushSection()

	if len(rawSections) == 0 {
		rawSections = append(rawSections, buildWikiSection(pageRel, "", pageRel, curHeading, "", modPath, sources))
	}

	return rawSections
}

func buildWikiSection(page, anchor, target, heading, body, modPath string, sources []string) wikiSection {
	titleTokens := append(tokenize(page), tokenize(heading)...)
	sourceTokens := tokenize(modPath)
	for _, src := range sources {
		sourceTokens = append(sourceTokens, tokenize(src)...)
	}
	bodyTokens := tokenize(body)

	docLen := titleWeight*float64(len(titleTokens)) +
		sourceWeight*float64(len(sourceTokens)) +
		bodyWeight*float64(len(bodyTokens))

	return wikiSection{
		page:        page,
		anchor:      anchor,
		target:      target,
		heading:     heading,
		body:        body,
		modPath:     modPath,
		sources:     sources,
		titleTerms:  termFreqMap(titleTokens),
		sourceTerms: termFreqMap(sourceTokens),
		bodyTerms:   termFreqMap(bodyTokens),
		docLen:      docLen,
	}
}

func scoreAndRankSections(sections []wikiSection, queryTokens, pathHints []string, limit int) []SearchResult {
	nDocs := float64(len(sections))
	totalLen := 0.0
	for _, s := range sections {
		totalLen += s.docLen
	}
	avgDL := totalLen / nDocs
	if avgDL <= 0 {
		avgDL = 1.0
	}

	docFreq := make(map[string]int, len(queryTokens))
	for _, qt := range queryTokens {
		for _, s := range sections {
			if s.titleTerms[qt] > 0 || s.sourceTerms[qt] > 0 || s.bodyTerms[qt] > 0 {
				docFreq[qt]++
			}
		}
	}

	var results []SearchResult
	for _, s := range sections {
		score := 0.0
		matchedTerms := 0

		for _, qt := range queryTokens {
			tf := titleWeight*float64(s.titleTerms[qt]) +
				sourceWeight*float64(s.sourceTerms[qt]) +
				bodyWeight*float64(s.bodyTerms[qt])
			if tf <= 0 {
				continue
			}
			matchedTerms++
			df := float64(docFreq[qt])
			idf := math.Log(1.0 + (nDocs-df+0.5)/(df+0.5))
			norm := tf * (bm25K1 + 1.0) / (tf + bm25K1*(1.0-bm25B+bm25B*(s.docLen/avgDL)))
			score += idf * norm
		}

		if score <= 0 {
			continue
		}

		if matchedTerms > 1 {
			score *= 1.0 + 0.25*float64(matchedTerms-1)
		}

		if len(pathHints) > 0 && matchesPathHints(s, pathHints) {
			score *= pathHintBoost
		}

		results = append(results, SearchResult{
			Page:    s.page,
			Anchor:  s.anchor,
			Target:  s.target,
			Heading: s.heading,
			Snippet: extractSnippet(s.body, s.heading, queryTokens),
			Sources: s.sources,
			Score:   score,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if math.Abs(results[i].Score-results[j].Score) > 1e-9 {
			return results[i].Score > results[j].Score
		}
		return results[i].Target < results[j].Target
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func cleanPathHint(p string) string {
	return strings.TrimPrefix(strings.ToLower(strings.Trim(filepath.ToSlash(strings.TrimSpace(p)), "/")), "./")
}

func matchesPathHints(s wikiSection, pathHints []string) bool {
	candidates := []string{cleanPathHint(s.page), cleanPathHint(s.modPath)}
	for _, src := range s.sources {
		candidates = append(candidates, cleanPathHint(src))
	}

	for _, rawHint := range pathHints {
		h := cleanPathHint(rawHint)
		if h == "" {
			continue
		}
		for _, cand := range candidates {
			if cand == "" {
				continue
			}
			if cand == "." {
				if h == "." {
					return true
				}
				continue
			}
			if strings.Contains(cand, h) || strings.Contains(h, cand) {
				return true
			}
		}
	}
	return false
}

func extractSnippet(body, fallbackHeading string, queryTokens []string) string {
	lines := strings.Split(body, "\n")
	inFence := false
	var fenceChar byte
	var fenceCount int
	var proseLines []string

	for _, rawLine := range lines {
		if !inFence {
			if ch, count, _, ok := tools.ParseFenceOpen(rawLine); ok {
				inFence = true
				fenceChar = ch
				fenceCount = count
				continue
			}
		} else {
			if tools.IsFenceClose(rawLine, fenceChar, fenceCount) {
				inFence = false
			}
			continue
		}

		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" ||
			strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, provenancePrefix) ||
			strings.HasPrefix(trimmed, "<!--") ||
			trimmed == "---" ||
			isTableSeparator(trimmed) {
			continue
		}
		proseLines = append(proseLines, trimmed)
	}

	for _, line := range proseLines {
		lineTerms := termFreqMap(tokenize(line))
		for _, qt := range queryTokens {
			if lineTerms[qt] > 0 {
				return tools.TruncateRunes(line, maxSnippetRunes)
			}
		}
	}
	if len(proseLines) > 0 {
		return tools.TruncateRunes(proseLines[0], maxSnippetRunes)
	}
	return tools.TruncateRunes(fallbackHeading, maxSnippetRunes)
}

func isTableSeparator(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "|") {
		return false
	}
	for _, r := range trimmed {
		if r != '|' && r != '-' && r != ':' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func extractSectionByAnchor(pagePath, content, targetAnchor string) (string, error) {
	lines := strings.Split(content, "\n")
	seenCounts := make(map[string]int)
	inFence := false
	var fenceChar byte
	var fenceCount int

	startLine := -1
	startLevel := 0
	endLine := len(lines)
	var availableAnchors []string

	for i, line := range lines {
		if !inFence {
			if ch, count, _, ok := tools.ParseFenceOpen(line); ok {
				inFence = true
				fenceChar = ch
				fenceCount = count
				continue
			}
		} else {
			if tools.IsFenceClose(line, fenceChar, fenceCount) {
				inFence = false
			}
			continue
		}

		level, headingText, ok := tools.ParseATXHeading(line)
		if !ok {
			continue
		}

		anchor := tools.NextHeadingAnchor(headingText, seenCounts)
		if anchor != "" {
			availableAnchors = append(availableAnchors, anchor)
		}

		if startLine == -1 {
			if anchor == targetAnchor {
				startLine = i
				startLevel = level
			}
		} else if level <= startLevel {
			endLine = i
			break
		}
	}

	if startLine == -1 {
		avail := "none"
		if len(availableAnchors) > 0 {
			avail = strings.Join(availableAnchors, ", ")
		}
		return "", fmt.Errorf("section anchor %q not found in %s (available anchors: %s)", targetAnchor, pagePath, avail)
	}

	return strings.TrimSpace(strings.Join(lines[startLine:endLine], "\n")), nil
}

func tokenize(s string) []string {
	rawWords := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var tokens []string
	for _, w := range rawWords {
		lower := strings.ToLower(w)
		tokens = append(tokens, lower)
		if subs := splitCamelCase(w); len(subs) > 1 {
			for _, sub := range subs {
				tokens = append(tokens, strings.ToLower(sub))
			}
		}
	}
	return tokens
}

func splitCamelCase(w string) []string {
	runes := []rune(w)
	var parts []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prevLowerCurrUpper := unicode.IsLower(runes[i-1]) && unicode.IsUpper(runes[i])
		acronymToWord := unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i]) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if prevLowerCurrUpper || acronymToWord {
			parts = append(parts, string(runes[start:i]))
			start = i
		}
	}
	parts = append(parts, string(runes[start:]))
	return parts
}

func uniqueTokens(tokens []string) []string {
	seen := make(map[string]bool, len(tokens))
	var out []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func termFreqMap(tokens []string) map[string]int {
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m
}
