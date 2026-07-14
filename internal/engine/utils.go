package engine

import (
	"path/filepath"
)

// toSafeMarkdownFilename converts a module path to a safe filename for markdown docs.
func toSafeMarkdownFilename(modulePath string) string {
	if modulePath == "." || modulePath == "" {
		return "README.md"
	}
	return filepath.Join(modulePath, "README.md")
}

func makeLogEvent(onEvent func(Event)) LogEventFunc {
	return func(t EventType, m string) {
		if onEvent != nil {
			onEvent(Event{Type: t, Message: m})
		}
	}
}
