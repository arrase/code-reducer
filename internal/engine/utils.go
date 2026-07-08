package engine

import (
	"path/filepath"
	"strings"
)

// ToSafeMarkdownFilename converts a module path to a safe filename for markdown docs.
func ToSafeMarkdownFilename(modulePath string) string {
	safeName := strings.ReplaceAll(modulePath, string(filepath.Separator), "_")
	if safeName == "." || safeName == "" {
		safeName = "root"
	}
	return safeName + ".md"
}
