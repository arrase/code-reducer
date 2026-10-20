package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/tools"
)

type FileCacheEntry struct {
	SHA256 string `json:"sha256"`
	Facts  string `json:"facts"`
}

const (
	currentCacheVersion = 1
	metadataFileName    = ".metadata.json"
)

type MetadataCache struct {
	Version   int                       `json:"version"`
	StepsHash string                    `json:"steps_hash"`
	Files     map[string]FileCacheEntry `json:"files"`
	Modules   map[string]string         `json:"modules"`
}

func computeStepsHash(cfg *config.Config) string {
	h := sha256.New()
	writeField := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	writeField(cfg.ModelID)
	writeField(cfg.Language)
	writeField(cfg.SystemPrompt)
	writeField(cfg.ModuleSynthesisPrompt)
	writeField(cfg.ArchitecturePrompt)
	writeField(cfg.QuickstartPrompt)
	writeField(cfg.FileFactConsolidationPrompt)
	for _, step := range cfg.ExtractionSteps {
		writeField(step.Name)
		writeField(step.Prompt)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newEmptyCache() *MetadataCache {
	return &MetadataCache{
		Version: currentCacheVersion,
		Files:   make(map[string]FileCacheEntry),
		Modules: make(map[string]string),
	}
}

func loadMetadataCache(repoRoot, docsDir string) (*MetadataCache, error) {
	metadataPath := filepath.Join(docsDir, metadataFileName)
	data, err := tools.ReadFileSafely(repoRoot, metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newEmptyCache(), nil
		}
		return nil, fmt.Errorf("failed to read metadata cache: %w", err)
	}
	var cache MetadataCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata cache: %w", err)
	}
	if cache.Version != currentCacheVersion {
		// Incompatible version: return a clean cache
		return newEmptyCache(), nil
	}
	if cache.Files == nil {
		cache.Files = make(map[string]FileCacheEntry)
	}
	if cache.Modules == nil {
		cache.Modules = make(map[string]string)
	}
	return &cache, nil
}

// IsInitialized checks if the metadata cache file exists.
func IsInitialized(repoRoot, docsDir string) bool {
	return tools.FileExistsSafely(repoRoot, filepath.Join(docsDir, metadataFileName))
}

func saveMetadataCache(repoRoot, docsDir string, cache *MetadataCache) error {
	cache.Version = currentCacheVersion
	metadataPath := filepath.Join(docsDir, metadataFileName)
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return tools.WriteFileSafely(repoRoot, metadataPath, data)
}

func computeSHA256(repoRoot, virtualPath string) (string, error) {
	content, err := tools.ReadFileSafely(repoRoot, virtualPath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:]), nil
}
