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

const currentCacheVersion = 1

type MetadataCache struct {
	Version   int                       `json:"version"`
	StepsHash string                    `json:"steps_hash"`
	Files     map[string]FileCacheEntry `json:"files"`
	Modules   map[string]string         `json:"modules"`
}

func computeStepsHash(steps []config.ExtractionStep) string {
	data, _ := json.Marshal(steps)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func newEmptyCache() *MetadataCache {
	return &MetadataCache{
		Version: currentCacheVersion,
		Files:   make(map[string]FileCacheEntry),
		Modules: make(map[string]string),
	}
}

func loadMetadataCache(repoRoot string, docsDir string) (*MetadataCache, error) {
	metadataPath := filepath.Join(docsDir, metadataFileName)
	data, err := tools.ReadFileSafely(repoRoot, metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newEmptyCache(), nil
		}
		return newEmptyCache(), fmt.Errorf("failed to read metadata cache: %w", err)
	}
	var cache MetadataCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return newEmptyCache(), fmt.Errorf("failed to unmarshal metadata cache: %w", err)
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
	metadataPath := filepath.Join(docsDir, metadataFileName)
	_, err := tools.ReadFileSafely(repoRoot, metadataPath)
	return err == nil
}

func saveMetadataCache(repoRoot string, docsDir string, cache *MetadataCache) error {
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
