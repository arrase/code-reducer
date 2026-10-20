package engine

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/arrase/code-reducer/internal/security"
	"github.com/arrase/code-reducer/internal/tools"
)

const (
	brokenLinkCommentPrefix = "<!-- code-reducer: broken internal link"
	mermaidFailedPrefix     = "<!-- code-reducer: mermaid parse failed"
	maxSummaryDescRunes     = 140
)

var validMermaidTypes = map[string]bool{
	"flowchart":       true,
	"graph":           true,
	"sequencediagram": true,
	"statediagram":    true,
	"statediagram-v2": true,
	"classdiagram":    true,
	"erdiagram":       true,
	"gantt":           true,
	"pie":             true,
	"gitgraph":        true,
	"mindmap":         true,
	"timeline":        true,
	"journey":         true,
	"c4context":       true,
	"xychart-beta":    true,
}

var mermaidNodeShapes = []struct {
	open  string
	close string
}{
	{"(((", ")))"},
	{"((", "))"},
	{"([", "])"},
	{"[(", ")]"},
	{"[[", "]]"},
	{"{{", "}}"},
	{"[", "]"},
	{"(", ")"},
	{"{", "}"},
}

var (
	reArrowToEnd   = regexp.MustCompile(`(-->|---|-\.->|==>|~~~)(\s*\|[^|]*\|)?\s*\bend\b`)
	reEndToArrow   = regexp.MustCompile(`\bend\b(\s*(?:-->|---|-\.->|==>|~~~|--))`)
	reEndShapeNode = regexp.MustCompile(`\bend\s*([\[\(\{])`)
	reLineAnchor   = regexp.MustCompile(`(?i)^L\d+(-L?\d+)?$`)
)

// finalizeWiki runs deterministic post-processing on generated markdown files in docsDir:
//  1. Sanitizes and heuristically validates Mermaid blocks (degrading broken blocks to text fences).
//  2. Generates the deterministic <docsDir>/index.md navigation entrypoint.
//  3. Auto-repairs relative links and stamps any remaining broken internal links or anchors.
func finalizeWiki(repoRoot, docsDir string, tree *DirNode, cache *MetadataCache, onStatus StatusFunc) error {
	onStatus("Finalizing Wiki: validating Mermaid diagrams, generating index, and checking links...")

	mdFiles, err := listMarkdownFiles(repoRoot, docsDir)
	if err != nil {
		return err
	}

	totalDegraded := 0
	for _, relPath := range mdFiles {
		rawBytes, err := tools.ReadFileSafely(repoRoot, relPath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", relPath, err)
		}
		original := string(rawBytes)
		updated, degraded := sanitizeAndValidateMermaid(original)
		totalDegraded += degraded
		if updated != original {
			if err := tools.WriteFileSafely(repoRoot, relPath, []byte(updated)); err != nil {
				return fmt.Errorf("failed to write sanitized markdown %s: %w", relPath, err)
			}
		}
	}
	if totalDegraded > 0 {
		onStatus(fmt.Sprintf("Warning: degraded %d invalid Mermaid block(s) to text fences", totalDegraded))
	}

	if err := generateWikiIndex(repoRoot, docsDir, tree, cache); err != nil {
		return err
	}

	brokenCount, err := validateMarkdownLinks(repoRoot, docsDir)
	if err != nil {
		return err
	}
	if brokenCount > 0 {
		onStatus(fmt.Sprintf("Warning: stamped %d broken internal link(s) in %s", brokenCount, docsDir))
	}

	return nil
}

func listMarkdownFiles(repoRoot, docsDir string) ([]string, error) {
	docsAbs, err := security.SafeResolve(repoRoot, docsDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(docsAbs); os.IsNotExist(err) {
		return nil, nil
	}

	rootAbs, err := security.SafeResolve(repoRoot, ".")
	if err != nil {
		return nil, err
	}

	var files []string
	err = filepath.WalkDir(docsAbs, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		rel, err := filepath.Rel(rootAbs, p)
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

// sanitizeAndValidateMermaid extracts fenced ```mermaid blocks, auto-sanitizes common
// small-LLM syntax errors (unquoted labels with special chars, 'end' node IDs), validates
// each block heuristically, and degrades invalid blocks into ```text fences with an HTML comment.
func sanitizeAndValidateMermaid(markdown string) (string, int) {
	lines := strings.Split(markdown, "\n")
	var out []string
	degradedCount := 0

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		fenceChar, fenceCount, info, ok := tools.ParseFenceOpen(line)
		if !ok {
			out = append(out, line)
			continue
		}

		infoFields := strings.Fields(info)
		isMermaid := len(infoFields) > 0 && strings.EqualFold(infoFields[0], "mermaid")

		var bodyLines []string
		closed := false
		var closeLine string
		for i = i + 1; i < len(lines); i++ {
			if tools.IsFenceClose(lines[i], fenceChar, fenceCount) {
				closed = true
				closeLine = lines[i]
				break
			}
			bodyLines = append(bodyLines, lines[i])
		}

		if !isMermaid {
			out = append(out, line)
			out = append(out, bodyLines...)
			if closed {
				out = append(out, closeLine)
			}
			continue
		}

		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		fence := strings.Repeat(string(fenceChar), fenceCount)
		rawBody := strings.Join(bodyLines, "\n")
		if !closed {
			degradedCount++
			out = append(out, fmt.Sprintf("%s%s (unclosed mermaid code fence) -->", indent, mermaidFailedPrefix))
			out = append(out, fmt.Sprintf("%s%stext", indent, fence))
			out = append(out, bodyLines...)
			continue
		}

		sanitizedBody := sanitizeMermaidBody(rawBody)
		if errMsg := heuristicMermaidError(sanitizedBody); errMsg != "" {
			degradedCount++
			out = append(out, fmt.Sprintf("%s%s (%s) -->", indent, mermaidFailedPrefix, errMsg))
			out = append(out, fmt.Sprintf("%s%stext", indent, fence))
			out = append(out, bodyLines...)
			out = append(out, closeLine)
			continue
		}

		out = append(out, line)
		out = append(out, strings.Split(sanitizedBody, "\n")...)
		out = append(out, closeLine)
	}

	return strings.Join(out, "\n"), degradedCount
}

func firstMermaidHeaderToken(body string) (string, int) {
	lines := strings.Split(body, "\n")
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		return strings.ToLower(fields[0]), idx
	}
	return "", -1
}

func isFlowchartType(diagramType string) bool {
	return diagramType == "flowchart" || diagramType == "graph"
}

func sanitizeMermaidBody(body string) string {
	diagramType, headerIdx := firstMermaidHeaderToken(body)
	if !isFlowchartType(diagramType) {
		return body
	}

	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if i <= headerIdx {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		lines[i] = sanitizeFlowchartLine(line)
	}
	return strings.Join(lines, "\n")
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func matchingCloseBracket(open byte) byte {
	switch open {
	case '[':
		return ']'
	case '(':
		return ')'
	case '{':
		return '}'
	default:
		return 0
	}
}

func splitNodeShape(shapeExpr string) (string, string, string) {
	for _, s := range mermaidNodeShapes {
		if len(shapeExpr) >= len(s.open)+len(s.close) &&
			strings.HasPrefix(shapeExpr, s.open) &&
			strings.HasSuffix(shapeExpr, s.close) {
			return s.open, shapeExpr[len(s.open) : len(shapeExpr)-len(s.close)], s.close
		}
	}
	return shapeExpr[:1], shapeExpr[1 : len(shapeExpr)-1], shapeExpr[len(shapeExpr)-1:]
}

func sanitizeFlowchartLine(line string) string {
	var b strings.Builder
	i := 0
	n := len(line)

	for i < n {
		if line[i] == '"' || line[i] == '|' {
			delim := line[i]
			b.WriteByte(delim)
			i++
			for i < n {
				b.WriteByte(line[i])
				if line[i] == delim && line[i-1] != '\\' {
					i++
					break
				}
				i++
			}
			continue
		}

		if isIdentChar(line[i]) {
			start := i
			for i < n && isIdentChar(line[i]) {
				i++
			}
			ident := line[start:i]

			if i < n && (line[i] == '[' || line[i] == '(' || line[i] == '{') {
				openChar := line[i]
				closeChar := matchingCloseBracket(openChar)
				closeIdx := findMatchingDelimiter(line, i, openChar, closeChar)
				if closeIdx != -1 {
					if ident == "end" {
						ident = "endNode"
					}
					openDelim, inner, closeDelim := splitNodeShape(line[i : closeIdx+1])
					b.WriteString(ident)
					b.WriteString(openDelim)
					b.WriteString(sanitizeNodeLabel(inner))
					b.WriteString(closeDelim)
					i = closeIdx + 1
					continue
				}
			}

			b.WriteString(ident)
			continue
		}

		b.WriteByte(line[i])
		i++
	}

	res := b.String()
	res = reArrowToEnd.ReplaceAllString(res, "$1$2 endNode")
	res = reEndToArrow.ReplaceAllString(res, "endNode$1")
	return res
}

func findMatchingDelimiter(s string, openIdx int, openChar, closeChar byte) int {
	depth := 0
	inQuotes := false
	for i := openIdx; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && (i == 0 || s[i-1] != '\\') {
			inQuotes = !inQuotes
			continue
		}
		if inQuotes {
			continue
		}
		if ch == openChar {
			depth++
		} else if ch == closeChar {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func sanitizeNodeLabel(inner string) string {
	trimmed := strings.TrimSpace(inner)
	if len(trimmed) >= 2 && strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		return inner
	}
	if !strings.ContainsAny(inner, "():;<>") {
		return inner
	}
	clean := strings.ReplaceAll(inner, ";", ",")
	clean = strings.ReplaceAll(clean, "<", "&lt;")
	clean = strings.ReplaceAll(clean, ">", "&gt;")
	clean = strings.ReplaceAll(clean, "\"", "'")
	return `"` + clean + `"`
}

func countUnescapedQuotes(s string) int {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '"' && (i == 0 || s[i-1] != '\\') {
			count++
		}
	}
	return count
}

// heuristicMermaidError validates a Mermaid diagram body without external tools.
// It returns a non-empty reason string if the diagram is invalid.
func heuristicMermaidError(body string) string {
	diagramType, headerIdx := firstMermaidHeaderToken(body)
	if diagramType == "" {
		return "empty mermaid block"
	}
	if !validMermaidTypes[diagramType] {
		lines := strings.Split(body, "\n")
		fields := strings.Fields(strings.TrimSpace(lines[headerIdx]))
		return fmt.Sprintf("unknown diagram type %q", fields[0])
	}

	lines := strings.Split(body, "\n")
	contentLines := 0
	isFlow := isFlowchartType(diagramType)

	for i := headerIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		contentLines++

		if countUnescapedQuotes(trimmed)%2 != 0 {
			return "unclosed double quote"
		}
		if !hasBalancedBrackets(trimmed, isFlow) {
			return "unbalanced brackets"
		}
		if isFlow {
			if hasFlowchartEndNodeID(trimmed) {
				return "reserved keyword 'end' used as node ID"
			}
			if hasUnquotedSpecialLabel(trimmed) {
				return "unquoted special characters in node label"
			}
		}
	}

	if contentLines == 0 {
		return "diagram has no content lines"
	}
	return ""
}

func hasBalancedBrackets(line string, checkBraces bool) bool {
	var stack []byte
	inQuotes := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if ch == '"' && (i == 0 || line[i-1] != '\\') {
			inQuotes = !inQuotes
			continue
		}
		if inQuotes {
			continue
		}
		switch ch {
		case '[', '(':
			stack = append(stack, matchingCloseBracket(ch))
		case '{':
			if checkBraces {
				stack = append(stack, '}')
			}
		case ']', ')':
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return false
			}
			stack = stack[:len(stack)-1]
		case '}':
			if checkBraces {
				if len(stack) == 0 || stack[len(stack)-1] != ch {
					return false
				}
				stack = stack[:len(stack)-1]
			}
		}
	}
	return len(stack) == 0
}

func hasFlowchartEndNodeID(line string) bool {
	return reArrowToEnd.MatchString(line) || reEndToArrow.MatchString(line) || reEndShapeNode.MatchString(line)
}

func hasUnquotedSpecialLabel(line string) bool {
	i := 0
	n := len(line)
	for i < n {
		if line[i] == '"' || line[i] == '|' {
			delim := line[i]
			i++
			for i < n {
				if line[i] == delim && line[i-1] != '\\' {
					i++
					break
				}
				i++
			}
			continue
		}
		if isIdentChar(line[i]) {
			for i < n && isIdentChar(line[i]) {
				i++
			}
			if i < n && (line[i] == '[' || line[i] == '(' || line[i] == '{') {
				openChar := line[i]
				closeChar := matchingCloseBracket(openChar)
				closeIdx := findMatchingDelimiter(line, i, openChar, closeChar)
				if closeIdx == -1 {
					return true
				}
				_, inner, _ := splitNodeShape(line[i : closeIdx+1])
				inner = strings.TrimSpace(inner)
				isQuoted := len(inner) >= 2 && strings.HasPrefix(inner, "\"") && strings.HasSuffix(inner, "\"")
				if !isQuoted && strings.ContainsAny(inner, ";<>") {
					return true
				}
				i = closeIdx + 1
				continue
			}
			continue
		}
		i++
	}
	return false
}

// generateWikiIndex deterministically generates <docsDir>/index.md with links to
// architecture.md, quickstart.md, and all documented modules.
func generateWikiIndex(repoRoot, docsDir string, tree *DirNode, cache *MetadataCache) error {
	var b strings.Builder
	b.WriteString("# Documentation Index\n\n")

	archExists := tools.FileExistsSafely(repoRoot, filepath.Join(docsDir, "architecture.md"))
	qsExists := tools.FileExistsSafely(repoRoot, filepath.Join(docsDir, "quickstart.md"))
	if archExists || qsExists {
		b.WriteString("## Overview\n\n")
		if archExists {
			b.WriteString("- [Architecture Overview](architecture.md)\n")
		}
		if qsExists {
			b.WriteString("- [Developer Quickstart](quickstart.md)\n")
		}
		b.WriteString("\n")
	}

	nodesByPath := collectTreeNodes(tree)
	var dirPaths []string
	for d := range nodesByPath {
		dirPaths = append(dirPaths, d)
	}
	sort.Strings(dirPaths)

	b.WriteString("## Modules\n\n")
	for _, dirPath := range dirPaths {
		relModPath := filepath.ToSlash(filepath.Join("modules", toSafeMarkdownFilename(dirPath)))
		fullModPath := filepath.Join(docsDir, relModPath)

		summary := strings.TrimSpace(cache.Modules[dirPath])
		if summary == "" && tools.FileExistsSafely(repoRoot, fullModPath) {
			if data, err := tools.ReadFileSafely(repoRoot, fullModPath); err == nil {
				summary = strings.TrimSpace(string(data))
			}
		}
		if summary == "" {
			continue
		}

		label := dirPath
		if dirPath == "." {
			label = "Root (.)"
		}

		desc := extractSummaryDescription(summary)
		filesInfo := formatNodeFiles(nodesByPath[dirPath])

		line := fmt.Sprintf("- [%s](%s)", label, relModPath)
		if desc != "" {
			line += " — " + desc
		}
		if filesInfo != "" {
			line += " (" + filesInfo + ")"
		}
		b.WriteString(line + "\n")
	}

	indexPath := filepath.Join(docsDir, "index.md")
	return tools.WriteFileSafely(repoRoot, indexPath, []byte(b.String()))
}

func extractSummaryDescription(summary string) string {
	lines := strings.Split(summary, "\n")
	inFence := false
	var fenceChar byte
	var fenceCount int

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
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, ">") ||
			strings.HasPrefix(trimmed, "```") ||
			strings.HasPrefix(trimmed, "~~~") ||
			strings.HasPrefix(trimmed, "|") ||
			strings.HasPrefix(trimmed, "-") ||
			strings.HasPrefix(trimmed, "*") ||
			strings.HasPrefix(trimmed, "<!--") {
			continue
		}

		return tools.TruncateRunes(trimmed, maxSummaryDescRunes)
	}
	return ""
}

func formatNodeFiles(node *DirNode) string {
	if len(node.Files) == 0 {
		return ""
	}
	names := make([]string, 0, len(node.Files))
	for _, f := range node.Files {
		names = append(names, path.Base(f))
	}
	sort.Strings(names)
	unit := "files"
	if len(names) == 1 {
		unit = "file"
	}
	return fmt.Sprintf("%d %s: %s", len(names), unit, strings.Join(names, ", "))
}

// validateMarkdownLinks walks all .md files under docsDir, strips prior broken-link stamps,
// auto-repairs relative module/doc links when unambiguous, and stamps any remaining broken
// internal links or heading anchors. Returns the total count of broken links found.
func validateMarkdownLinks(repoRoot, docsDir string) (int, error) {
	mdFiles, err := listMarkdownFiles(repoRoot, docsDir)
	if err != nil {
		return 0, err
	}

	rawContents := make(map[string]string, len(mdFiles))
	cleanContents := make(map[string]string, len(mdFiles))
	anchorsByFile := make(map[string]map[string]bool, len(mdFiles))

	for _, relPath := range mdFiles {
		data, err := tools.ReadFileSafely(repoRoot, relPath)
		if err != nil {
			return 0, fmt.Errorf("failed to read %s: %w", relPath, err)
		}
		raw := string(data)
		cleaned := stripBrokenLinkStamps(raw)
		rawContents[relPath] = raw
		cleanContents[relPath] = cleaned
		anchorsByFile[relPath] = extractHeadingAnchors(cleaned)
	}

	rootAbs, err := security.SafeResolve(repoRoot, ".")
	if err != nil {
		return 0, err
	}

	totalBroken := 0
	for _, relPath := range mdFiles {
		cleaned := cleanContents[relPath]
		rawLines := strings.Split(cleaned, "\n")
		sourceDir := path.Dir(relPath)

		inFence := false
		var fenceChar byte
		var fenceCount int
		var outLines []string

		for _, rawLine := range rawLines {
			if !inFence {
				if ch, count, _, ok := tools.ParseFenceOpen(rawLine); ok {
					inFence = true
					fenceChar = ch
					fenceCount = count
					outLines = append(outLines, rawLine)
					continue
				}
			} else {
				if tools.IsFenceClose(rawLine, fenceChar, fenceCount) {
					inFence = false
				}
				outLines = append(outLines, rawLine)
				continue
			}

			matches := findMarkdownLinks(rawLine)
			if len(matches) == 0 {
				outLines = append(outLines, rawLine)
				continue
			}

			updatedLine := rawLine
			var lineStamps []string

			for i := len(matches) - 1; i >= 0; i-- {
				m := matches[i]
				rawHref := strings.TrimSpace(rawLine[m.hrefStart:m.hrefEnd])
				hrefTarget := extractLinkTarget(rawHref)
				if hrefTarget == "" || isExternalLink(hrefTarget) {
					continue
				}

				linkPath, anchor, hasHash := strings.Cut(hrefTarget, "#")
				if strings.HasPrefix(linkPath, "/") {
					lineStamps = append(lineStamps, fmt.Sprintf("%s [%s] root-absolute link is not portable; use a relative path -->", brokenLinkCommentPrefix, hrefTarget))
					continue
				}

				var targetRelPath string
				if linkPath == "" {
					targetRelPath = relPath
				} else {
					joinedRel := filepath.Join(filepath.FromSlash(sourceDir), filepath.FromSlash(linkPath))
					resolvedAbs, resolveErr := security.SafeResolve(repoRoot, joinedRel)
					exists := false
					if resolveErr == nil {
						if info, statErr := os.Stat(resolvedAbs); statErr == nil && !info.IsDir() {
							exists = true
							if relToRoot, relErr := filepath.Rel(rootAbs, resolvedAbs); relErr == nil {
								targetRelPath = filepath.ToSlash(relToRoot)
							}
						}
					}

					if !exists {
						if repairedLink, repairedRel, ok := tryAutoRepairLink(repoRoot, docsDir, sourceDir, linkPath); ok {
							newHref := repairedLink
							if hasHash {
								newHref = repairedLink + "#" + anchor
							}
							updatedLine = updatedLine[:m.hrefStart] + newHref + updatedLine[m.hrefEnd:]
							hrefTarget = newHref
							linkPath = repairedLink
							targetRelPath = repairedRel
							exists = true
						}
					}

					if !exists {
						lineStamps = append(lineStamps, fmt.Sprintf("%s [%s] file %q does not exist -->", brokenLinkCommentPrefix, hrefTarget, linkPath))
						continue
					}
				}

				if anchor != "" && strings.EqualFold(path.Ext(targetRelPath), ".md") && !reLineAnchor.MatchString(anchor) {
					targetAnchors, known := anchorsByFile[targetRelPath]
					if !known {
						if data, readErr := tools.ReadFileSafely(repoRoot, targetRelPath); readErr == nil {
							targetAnchors = extractHeadingAnchors(string(data))
							anchorsByFile[targetRelPath] = targetAnchors
						}
					}
					if !targetAnchors[strings.ToLower(anchor)] {
						displayPath := linkPath
						if displayPath == "" {
							displayPath = path.Base(relPath)
						}
						lineStamps = append(lineStamps, fmt.Sprintf("%s [%s] heading anchor %q does not exist in %q -->", brokenLinkCommentPrefix, hrefTarget, "#"+anchor, displayPath))
					}
				}
			}

			for i := len(lineStamps) - 1; i >= 0; i-- {
				outLines = append(outLines, lineStamps[i])
				totalBroken++
			}
			outLines = append(outLines, updatedLine)
		}

		finalContent := strings.Join(outLines, "\n")
		if finalContent != rawContents[relPath] {
			if err := tools.WriteFileSafely(repoRoot, relPath, []byte(finalContent)); err != nil {
				return totalBroken, fmt.Errorf("failed to write %s: %w", relPath, err)
			}
		}
	}

	return totalBroken, nil
}

func stripBrokenLinkStamps(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, brokenLinkCommentPrefix) && strings.HasSuffix(trimmed, "-->") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func extractHeadingAnchors(markdown string) map[string]bool {
	anchors := make(map[string]bool)
	seenCounts := make(map[string]int)
	lines := strings.Split(markdown, "\n")
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

		_, title, ok := tools.ParseATXHeading(line)
		if !ok {
			continue
		}
		if anchor := tools.NextHeadingAnchor(title, seenCounts); anchor != "" {
			anchors[anchor] = true
		}
	}
	return anchors
}

type linkMatch struct {
	hrefStart int
	hrefEnd   int
}

func findMarkdownLinks(line string) []linkMatch {
	var matches []linkMatch
	n := len(line)
	for i := 0; i < n; i++ {
		if line[i] == '`' {
			tickStart := i
			for i < n && line[i] == '`' {
				i++
			}
			tickLen := i - tickStart
			j := i
			closed := false
			for j < n {
				if line[j] != '`' {
					j++
					continue
				}
				runStart := j
				for j < n && line[j] == '`' {
					j++
				}
				if j-runStart == tickLen {
					i = j - 1
					closed = true
					break
				}
			}
			if !closed {
				i = tickStart + tickLen - 1
			}
			continue
		}

		if line[i] != '[' {
			continue
		}
		if i > 0 && line[i-1] == '!' {
			continue
		}

		closeBracket := -1
		for j := i + 1; j < n; j++ {
			if line[j] == '[' {
				break
			}
			if line[j] == ']' {
				closeBracket = j
				break
			}
		}
		if closeBracket == -1 || closeBracket+1 >= n || line[closeBracket+1] != '(' {
			continue
		}

		openParen := closeBracket + 1
		closeParen := findMatchingDelimiter(line, openParen, '(', ')')
		if closeParen == -1 {
			continue
		}

		matches = append(matches, linkMatch{
			hrefStart: openParen + 1,
			hrefEnd:   closeParen,
		})
		i = closeParen
	}
	return matches
}

func extractLinkTarget(rawHref string) string {
	trimmed := strings.TrimSpace(rawHref)
	if trimmed == "" {
		return ""
	}
	fields := strings.Fields(trimmed)
	target := fields[0]
	target = strings.TrimPrefix(target, "<")
	target = strings.TrimSuffix(target, ">")
	return target
}

func isExternalLink(href string) bool {
	lower := strings.ToLower(href)
	return strings.Contains(lower, "://") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "tel:") ||
		strings.HasPrefix(lower, "data:")
}

func tryAutoRepairLink(repoRoot, docsDir, sourceDir, linkPath string) (string, string, bool) {
	cleaned := strings.TrimPrefix(path.Clean(linkPath), "./")
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "..") {
		return "", "", false
	}

	docsSlash := strings.Trim(filepath.ToSlash(docsDir), "/")
	stripped := strings.TrimPrefix(cleaned, docsSlash+"/")
	stripped = strings.TrimPrefix(stripped, "modules/")

	candidates := []string{
		path.Join(docsSlash, "modules", stripped),
		path.Join(docsSlash, "modules", stripped, "README.md"),
		path.Join(docsSlash, strings.TrimPrefix(cleaned, docsSlash+"/")),
		path.Join(docsSlash, strings.TrimPrefix(cleaned, docsSlash+"/"), "README.md"),
		cleaned,
	}

	rootAbs, err := security.SafeResolve(repoRoot, ".")
	if err != nil {
		return "", "", false
	}
	sourceAbs, err := security.SafeResolve(repoRoot, filepath.FromSlash(sourceDir))
	if err != nil {
		return "", "", false
	}

	for _, cand := range candidates {
		candAbs, err := security.SafeResolve(repoRoot, filepath.FromSlash(cand))
		if err != nil {
			continue
		}
		info, err := os.Stat(candAbs)
		if err != nil || info.IsDir() {
			continue
		}
		relFromSource, err := filepath.Rel(sourceAbs, candAbs)
		if err != nil {
			continue
		}
		relFromRoot, err := filepath.Rel(rootAbs, candAbs)
		if err != nil {
			continue
		}
		return filepath.ToSlash(relFromSource), filepath.ToSlash(relFromRoot), true
	}

	return "", "", false
}
