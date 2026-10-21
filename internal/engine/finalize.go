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

		bodyLines, closeLine, closed, nextIdx := collectFencedBlock(lines, i, fenceChar, fenceCount)
		i = nextIdx

		if !isMermaidFence(info) {
			out = append(out, line)
			out = append(out, bodyLines...)
			if closed {
				out = append(out, closeLine)
			}
			continue
		}

		blockLines, degraded := renderMermaidFence(line, fenceChar, fenceCount, bodyLines, closeLine, closed)
		out = append(out, blockLines...)
		if degraded {
			degradedCount++
		}
	}

	return strings.Join(out, "\n"), degradedCount
}

func isMermaidFence(info string) bool {
	fields := strings.Fields(info)
	return len(fields) > 0 && strings.EqualFold(fields[0], "mermaid")
}

func collectFencedBlock(lines []string, openIdx int, fenceChar byte, fenceCount int) ([]string, string, bool, int) {
	var bodyLines []string
	for i := openIdx + 1; i < len(lines); i++ {
		if tools.IsFenceClose(lines[i], fenceChar, fenceCount) {
			return bodyLines, lines[i], true, i
		}
		bodyLines = append(bodyLines, lines[i])
	}
	return bodyLines, "", false, len(lines)
}

func renderMermaidFence(openLine string, fenceChar byte, fenceCount int, bodyLines []string, closeLine string, closed bool) ([]string, bool) {
	indent := openLine[:len(openLine)-len(strings.TrimLeft(openLine, " \t"))]
	fence := strings.Repeat(string(fenceChar), fenceCount)
	if !closed {
		out := []string{
			fmt.Sprintf("%s%s (unclosed mermaid code fence) -->", indent, mermaidFailedPrefix),
			fmt.Sprintf("%s%stext", indent, fence),
		}
		return append(out, bodyLines...), true
	}

	sanitizedBody := sanitizeMermaidBody(strings.Join(bodyLines, "\n"))
	if errMsg := heuristicMermaidError(sanitizedBody); errMsg != "" {
		out := []string{
			fmt.Sprintf("%s%s (%s) -->", indent, mermaidFailedPrefix, errMsg),
			fmt.Sprintf("%s%stext", indent, fence),
		}
		out = append(out, bodyLines...)
		return append(out, closeLine), true
	}

	out := []string{openLine}
	out = append(out, strings.Split(sanitizedBody, "\n")...)
	return append(out, closeLine), false
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
			end := skipDelimitedSegment(line, i)
			b.WriteString(line[i:end])
			i = end
			continue
		}

		if isIdentChar(line[i]) {
			nodeExpr, nextIdx := consumeFlowchartIdentOrNode(line, i)
			b.WriteString(nodeExpr)
			i = nextIdx
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

func skipDelimitedSegment(s string, start int) int {
	delim := s[start]
	for i := start + 1; i < len(s); i++ {
		if s[i] == delim && s[i-1] != '\\' {
			return i + 1
		}
	}
	return len(s)
}

func consumeFlowchartIdentOrNode(line string, start int) (string, int) {
	i := start
	for i < len(line) && isIdentChar(line[i]) {
		i++
	}
	ident := line[start:i]

	closeChar := matchingCloseBracket(peekByte(line, i))
	if closeChar == 0 {
		return ident, i
	}

	closeIdx := findMatchingDelimiter(line, i, line[i], closeChar)
	if closeIdx == -1 {
		return ident, i
	}

	if ident == "end" {
		ident = "endNode"
	}
	openDelim, inner, closeDelim := splitNodeShape(line[i : closeIdx+1])
	return ident + openDelim + sanitizeNodeLabel(inner) + closeDelim, closeIdx + 1
}

func peekByte(s string, idx int) byte {
	if idx < len(s) {
		return s[idx]
	}
	return 0
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
		if errMsg := validateMermaidContentLine(trimmed, isFlow); errMsg != "" {
			return errMsg
		}
	}

	if contentLines == 0 {
		return "diagram has no content lines"
	}
	return ""
}

func validateMermaidContentLine(trimmed string, isFlow bool) string {
	if countUnescapedQuotes(trimmed)%2 != 0 {
		return "unclosed double quote"
	}
	if !hasBalancedBrackets(trimmed, isFlow) {
		return "unbalanced brackets"
	}
	if !isFlow {
		return ""
	}
	if hasFlowchartEndNodeID(trimmed) {
		return "reserved keyword 'end' used as node ID"
	}
	if hasUnquotedSpecialLabel(trimmed) {
		return "unquoted special characters in node label"
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
		if closeCh := expectedCloseBracket(ch, checkBraces); closeCh != 0 {
			stack = append(stack, closeCh)
			continue
		}
		if isTrackedCloseBracket(ch, checkBraces) {
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

func expectedCloseBracket(ch byte, checkBraces bool) byte {
	if ch == '{' && !checkBraces {
		return 0
	}
	return matchingCloseBracket(ch)
}

func isTrackedCloseBracket(ch byte, checkBraces bool) bool {
	return ch == ']' || ch == ')' || (checkBraces && ch == '}')
}

func hasFlowchartEndNodeID(line string) bool {
	return reArrowToEnd.MatchString(line) || reEndToArrow.MatchString(line) || reEndShapeNode.MatchString(line)
}

func hasUnquotedSpecialLabel(line string) bool {
	i := 0
	n := len(line)
	for i < n {
		if line[i] == '"' || line[i] == '|' {
			i = skipDelimitedSegment(line, i)
			continue
		}
		if isIdentChar(line[i]) {
			bad, nextIdx := checkIdentNodeLabel(line, i)
			if bad {
				return true
			}
			i = nextIdx
			continue
		}
		i++
	}
	return false
}

func checkIdentNodeLabel(line string, start int) (bool, int) {
	i := start
	for i < len(line) && isIdentChar(line[i]) {
		i++
	}
	closeChar := matchingCloseBracket(peekByte(line, i))
	if closeChar == 0 {
		return false, i
	}
	closeIdx := findMatchingDelimiter(line, i, line[i], closeChar)
	if closeIdx == -1 {
		return true, len(line)
	}
	_, inner, _ := splitNodeShape(line[i : closeIdx+1])
	inner = strings.TrimSpace(inner)
	isQuoted := len(inner) >= 2 && strings.HasPrefix(inner, "\"") && strings.HasSuffix(inner, "\"")
	if !isQuoted && strings.ContainsAny(inner, ";<>") {
		return true, closeIdx + 1
	}
	return false, closeIdx + 1
}

// generateWikiIndex deterministically generates <docsDir>/index.md with links to
// architecture.md, quickstart.md, and all documented modules.
func generateWikiIndex(repoRoot, docsDir string, tree *DirNode, cache *MetadataCache) error {
	var b strings.Builder
	b.WriteString("# Documentation Index\n\n")
	writeIndexOverview(&b, repoRoot, docsDir)

	nodesByPath := collectTreeNodes(tree)
	var dirPaths []string
	for d := range nodesByPath {
		dirPaths = append(dirPaths, d)
	}
	sort.Strings(dirPaths)

	b.WriteString("## Modules\n\n")
	for _, dirPath := range dirPaths {
		if line := formatModuleIndexLine(repoRoot, docsDir, dirPath, nodesByPath[dirPath], cache); line != "" {
			b.WriteString(line + "\n")
		}
	}

	indexPath := filepath.Join(docsDir, "index.md")
	return tools.WriteFileSafely(repoRoot, indexPath, []byte(b.String()))
}

func writeIndexOverview(b *strings.Builder, repoRoot, docsDir string) {
	archExists := tools.FileExistsSafely(repoRoot, filepath.Join(docsDir, "architecture.md"))
	qsExists := tools.FileExistsSafely(repoRoot, filepath.Join(docsDir, "quickstart.md"))
	if !archExists && !qsExists {
		return
	}
	b.WriteString("## Overview\n\n")
	if archExists {
		b.WriteString("- [Architecture Overview](architecture.md)\n")
	}
	if qsExists {
		b.WriteString("- [Developer Quickstart](quickstart.md)\n")
	}
	b.WriteString("\n")
}

func formatModuleIndexLine(repoRoot, docsDir, dirPath string, node *DirNode, cache *MetadataCache) string {
	relModPath := filepath.ToSlash(filepath.Join("modules", toSafeMarkdownFilename(dirPath)))
	summary := resolveModuleSummary(repoRoot, filepath.Join(docsDir, relModPath), cache.Modules[dirPath])
	if summary == "" {
		return ""
	}

	label := dirPath
	if dirPath == "." {
		label = "Root (.)"
	}

	line := fmt.Sprintf("- [%s](%s)", label, relModPath)
	if desc := extractSummaryDescription(summary); desc != "" {
		line += " — " + desc
	}
	if filesInfo := formatNodeFiles(node); filesInfo != "" {
		line += " (" + filesInfo + ")"
	}
	return line
}

func resolveModuleSummary(repoRoot, fullModPath, cachedSummary string) string {
	if summary := strings.TrimSpace(cachedSummary); summary != "" {
		return summary
	}
	if !tools.FileExistsSafely(repoRoot, fullModPath) {
		return ""
	}
	data, err := tools.ReadFileSafely(repoRoot, fullModPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func extractSummaryDescription(summary string) string {
	lines := strings.Split(summary, "\n")
	var fs fenceState

	for _, rawLine := range lines {
		if fs.consume(rawLine) {
			continue
		}

		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || isNonParagraphLine(trimmed) {
			continue
		}

		return tools.TruncateRunes(trimmed, maxSummaryDescRunes)
	}
	return ""
}

var nonParagraphPrefixes = []string{"#", ">", "```", "~~~", "|", "-", "*", "<!--"}

func isNonParagraphLine(trimmed string) bool {
	for _, prefix := range nonParagraphPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

type fenceState struct {
	inFence bool
	char    byte
	count   int
}

func (f *fenceState) consume(line string) bool {
	if !f.inFence {
		if ch, count, _, ok := tools.ParseFenceOpen(line); ok {
			f.inFence = true
			f.char = ch
			f.count = count
			return true
		}
		return false
	}
	if tools.IsFenceClose(line, f.char, f.count) {
		f.inFence = false
	}
	return true
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
		finalContent, broken := processFileLinks(repoRoot, docsDir, rootAbs, relPath, cleanContents[relPath], anchorsByFile)
		totalBroken += broken
		if finalContent != rawContents[relPath] {
			if err := tools.WriteFileSafely(repoRoot, relPath, []byte(finalContent)); err != nil {
				return totalBroken, fmt.Errorf("failed to write %s: %w", relPath, err)
			}
		}
	}

	return totalBroken, nil
}

func processFileLinks(repoRoot, docsDir, rootAbs, relPath, cleaned string, anchorsByFile map[string]map[string]bool) (string, int) {
	rawLines := strings.Split(cleaned, "\n")
	sourceDir := path.Dir(relPath)

	var fs fenceState
	var outLines []string
	totalBroken := 0

	for _, rawLine := range rawLines {
		if fs.consume(rawLine) {
			outLines = append(outLines, rawLine)
			continue
		}

		updatedLine, lineStamps := processLineLinks(repoRoot, docsDir, rootAbs, relPath, sourceDir, rawLine, anchorsByFile)
		for i := len(lineStamps) - 1; i >= 0; i-- {
			outLines = append(outLines, lineStamps[i])
			totalBroken++
		}
		outLines = append(outLines, updatedLine)
	}

	return strings.Join(outLines, "\n"), totalBroken
}

func processLineLinks(repoRoot, docsDir, rootAbs, relPath, sourceDir, rawLine string, anchorsByFile map[string]map[string]bool) (string, []string) {
	matches := findMarkdownLinks(rawLine)
	if len(matches) == 0 {
		return rawLine, nil
	}

	updatedLine := rawLine
	var lineStamps []string

	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		rawHref := strings.TrimSpace(rawLine[m.hrefStart:m.hrefEnd])
		newHref, replaced, stamp := validateSingleLink(repoRoot, docsDir, rootAbs, relPath, sourceDir, rawHref, anchorsByFile)
		if replaced {
			updatedLine = updatedLine[:m.hrefStart] + newHref + updatedLine[m.hrefEnd:]
		}
		if stamp != "" {
			lineStamps = append(lineStamps, stamp)
		}
	}

	return updatedLine, lineStamps
}

func validateSingleLink(repoRoot, docsDir, rootAbs, relPath, sourceDir, rawHref string, anchorsByFile map[string]map[string]bool) (string, bool, string) {
	hrefTarget := extractLinkTarget(rawHref)
	if hrefTarget == "" || isExternalLink(hrefTarget) {
		return "", false, ""
	}

	linkPath, anchor, hasHash := strings.Cut(hrefTarget, "#")
	if strings.HasPrefix(linkPath, "/") {
		return "", false, fmt.Sprintf("%s [%s] root-absolute link is not portable; use a relative path -->", brokenLinkCommentPrefix, hrefTarget)
	}

	targetRelPath, repairedLink, repaired, ok := resolveLinkPath(repoRoot, docsDir, rootAbs, relPath, sourceDir, linkPath)
	if !ok {
		return "", false, fmt.Sprintf("%s [%s] file %q does not exist -->", brokenLinkCommentPrefix, hrefTarget, linkPath)
	}

	newHref := ""
	if repaired {
		linkPath = repairedLink
		newHref = repairedLink
		if hasHash {
			newHref = repairedLink + "#" + anchor
		}
		hrefTarget = newHref
	}

	stamp := validateAnchor(repoRoot, relPath, targetRelPath, linkPath, hrefTarget, anchor, anchorsByFile)
	return newHref, repaired, stamp
}

func resolveLinkPath(repoRoot, docsDir, rootAbs, relPath, sourceDir, linkPath string) (string, string, bool, bool) {
	if linkPath == "" {
		return relPath, "", false, true
	}

	joinedRel := filepath.Join(filepath.FromSlash(sourceDir), filepath.FromSlash(linkPath))
	if resolvedAbs, err := security.SafeResolve(repoRoot, joinedRel); err == nil {
		if info, statErr := os.Stat(resolvedAbs); statErr == nil && !info.IsDir() {
			var targetRelPath string
			if relToRoot, relErr := filepath.Rel(rootAbs, resolvedAbs); relErr == nil {
				targetRelPath = filepath.ToSlash(relToRoot)
			}
			return targetRelPath, "", false, true
		}
	}

	if repairedLink, repairedRel, ok := tryAutoRepairLink(repoRoot, docsDir, sourceDir, linkPath); ok {
		return repairedRel, repairedLink, true, true
	}

	return "", "", false, false
}

func validateAnchor(repoRoot, relPath, targetRelPath, linkPath, hrefTarget, anchor string, anchorsByFile map[string]map[string]bool) string {
	if anchor == "" || !strings.EqualFold(path.Ext(targetRelPath), ".md") || reLineAnchor.MatchString(anchor) {
		return ""
	}

	targetAnchors := getFileAnchors(repoRoot, targetRelPath, anchorsByFile)
	if targetAnchors[strings.ToLower(anchor)] {
		return ""
	}

	displayPath := linkPath
	if displayPath == "" {
		displayPath = path.Base(relPath)
	}
	return fmt.Sprintf("%s [%s] heading anchor %q does not exist in %q -->", brokenLinkCommentPrefix, hrefTarget, "#"+anchor, displayPath)
}

func getFileAnchors(repoRoot, targetRelPath string, anchorsByFile map[string]map[string]bool) map[string]bool {
	if targetAnchors, known := anchorsByFile[targetRelPath]; known {
		return targetAnchors
	}
	data, err := tools.ReadFileSafely(repoRoot, targetRelPath)
	if err != nil {
		return nil
	}
	targetAnchors := extractHeadingAnchors(string(data))
	anchorsByFile[targetRelPath] = targetAnchors
	return targetAnchors
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
	var fs fenceState

	for _, line := range lines {
		if fs.consume(line) {
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
			i = skipInlineCodeSpan(line, i)
			continue
		}

		m, ok := matchLinkAt(line, i)
		if !ok {
			continue
		}
		matches = append(matches, m)
		i = m.hrefEnd
	}
	return matches
}

func skipInlineCodeSpan(line string, tickStart int) int {
	n := len(line)
	i := tickStart
	for i < n && line[i] == '`' {
		i++
	}
	tickLen := i - tickStart

	for j := i; j < n; {
		if line[j] != '`' {
			j++
			continue
		}
		runStart := j
		for j < n && line[j] == '`' {
			j++
		}
		if j-runStart == tickLen {
			return j - 1
		}
	}
	return tickStart + tickLen - 1
}

func matchLinkAt(line string, start int) (linkMatch, bool) {
	if line[start] != '[' || (start > 0 && line[start-1] == '!') {
		return linkMatch{}, false
	}

	closeBracket := findLinkTextEnd(line, start+1)
	if closeBracket == -1 || closeBracket+1 >= len(line) || line[closeBracket+1] != '(' {
		return linkMatch{}, false
	}

	openParen := closeBracket + 1
	closeParen := findMatchingDelimiter(line, openParen, '(', ')')
	if closeParen == -1 {
		return linkMatch{}, false
	}

	return linkMatch{
		hrefStart: openParen + 1,
		hrefEnd:   closeParen,
	}, true
}

func findLinkTextEnd(line string, start int) int {
	for j := start; j < len(line); j++ {
		if line[j] == '[' {
			return -1
		}
		if line[j] == ']' {
			return j
		}
	}
	return -1
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
