package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arrase/code-reducer/internal/tools"
)

type FileCacheEntry struct {
	SHA256 string `json:"sha256"`
	Facts  string `json:"facts"`
}

type MetadataCache struct {
	LastDocumentedCommit string                    `json:"last_documented_commit"`
	Files                map[string]FileCacheEntry `json:"files"`
	Modules              map[string]string         `json:"modules"`
}

func loadMetadataCache(repoRoot string, docsDir string) (*MetadataCache, error) {
	metadataPath := filepath.Join(docsDir, ".metadata.json")
	data, err := tools.ReadFileSafely(repoRoot, metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &MetadataCache{
				Files:   make(map[string]FileCacheEntry),
				Modules: make(map[string]string),
			}, nil
		}
		return nil, fmt.Errorf("failed to read metadata cache: %w", err)
	}
	var cache MetadataCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata cache: %w", err)
	}
	if cache.Files == nil {
		cache.Files = make(map[string]FileCacheEntry)
	}
	if cache.Modules == nil {
		cache.Modules = make(map[string]string)
	}
	return &cache, nil
}

func saveMetadataCache(repoRoot string, docsDir string, cache *MetadataCache) error {
	metadataPath := filepath.Join(docsDir, ".metadata.json")
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
