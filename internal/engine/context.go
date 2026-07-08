package engine

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/arrase/code-reducer/internal/tools"
)

// BM25 parameters
const (
	k1 = 1.5
	b  = 0.75
)

// Document represents a file's content and its term frequency statistics.
type Document struct {
	Path       string
	Content    string
	Tokens     []string
	TermFreqs  map[string]int
	TokenCount int
}

// Tokenize splits a string into lowercase alphanumeric tokens.
func Tokenize(text string) []string {
	// Simple regex tokenizer for alphanumeric words
	re := regexp.MustCompile(`[a-zA-Z0-9]+`)
	matches := re.FindAllString(strings.ToLower(text), -1)
	return matches
}

// FilterFilesBM25 ranks the files in repoRoot based on their relevance to the query.
// It returns the topK file paths.
func FilterFilesBM25(repoRoot string, files []string, query string, topK int) ([]string, error) {
	if len(files) == 0 || query == "" {
		return files, nil
	}

	queryTokens := Tokenize(query)
	if len(queryTokens) == 0 {
		return files, nil
	}

	// 1. Load and tokenize all documents
	var docs []Document
	var totalDocLength int
	docFreqs := make(map[string]int)

	for _, file := range files {
		contentBytes, err := tools.ReadFileSafely(repoRoot, file)
		if err != nil {
			continue // Skip unreadable files
		}
		content := string(contentBytes)
		tokens := Tokenize(content)
		if len(tokens) == 0 {
			continue
		}

		termFreqs := make(map[string]int)
		uniqueTerms := make(map[string]bool)
		for _, t := range tokens {
			termFreqs[t]++
			uniqueTerms[t] = true
		}

		for term := range uniqueTerms {
			docFreqs[term]++
		}

		docs = append(docs, Document{
			Path:       file,
			Content:    content,
			Tokens:     tokens,
			TermFreqs:  termFreqs,
			TokenCount: len(tokens),
		})
		totalDocLength += len(tokens)
	}

	if len(docs) == 0 {
		return nil, nil
	}

	avgdl := float64(totalDocLength) / float64(len(docs))
	N := float64(len(docs))

	// 2. Compute IDF for query terms
	idfs := make(map[string]float64)
	for _, term := range queryTokens {
		df := float64(docFreqs[term])
		// Smoothed IDF
		idf := math.Log((N-df+0.5)/(df+0.5) + 1.0)
		if idf < 0 {
			idf = 0.0001 // Prevent negative idf
		}
		idfs[term] = idf
	}

	// 3. Score each document
	type DocScore struct {
		Path  string
		Score float64
	}
	var scores []DocScore

	for _, doc := range docs {
		var score float64
		dl := float64(doc.TokenCount)

		for _, term := range queryTokens {
			tf := float64(doc.TermFreqs[term])
			if tf == 0 {
				continue
			}

			idf := idfs[term]
			numerator := tf * (k1 + 1)
			denominator := tf + k1*(1-b+b*(dl/avgdl))
			score += idf * (numerator / denominator)
		}

		scores = append(scores, DocScore{
			Path:  doc.Path,
			Score: score,
		})
	}

	// 4. Sort by score descending
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	// 5. Select topK
	if topK > len(scores) {
		topK = len(scores)
	}

	result := make([]string, topK)
	for i := 0; i < topK; i++ {
		result[i] = scores[i].Path
	}

	return result, nil
}

// EstimateTokens counts tokens approximately based on characters (1 token ~ 4 characters).
func EstimateTokens(text string) int {
	return len(text) / 4
}

// WrapInXmlDelimiter wraps file content in strict XML-like tags to prevent prompt injection.
func WrapInXmlDelimiter(filePath string, content string) string {
	return "\n<file_content path=\"" + filePath + "\">\n" + content + "\n</file_content>\n"
}

// AutoScaleContext limits the maximum characters of context provided to LLM based on system memory or parameters.
func AutoScaleContext(maxCtx int) int {
	if maxCtx <= 0 {
		return 8192 // Safe default
	}
	return maxCtx
}
