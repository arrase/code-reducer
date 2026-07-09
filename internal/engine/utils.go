package engine

import (
	"strings"
)

// ToSafeMarkdownFilename converts a module path to a safe filename for markdown docs.
func ToSafeMarkdownFilename(modulePath string) string {
	safeName := strings.ReplaceAll(modulePath, "/", "_")
	if safeName == "." || safeName == "" {
		safeName = "root"
	}
	return safeName + ".md"
}
