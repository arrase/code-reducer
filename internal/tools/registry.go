package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// HashRepoRoot returns a SHA-256 hash of the resolved absolute path of the repository.
func HashRepoRoot(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	hash := sha256.New()
	hash.Write([]byte(abs))
	return hex.EncodeToString(hash.Sum(nil))
}
