package engine

import (
	"strings"
)

// toSafeMarkdownFilename converts a module path to a safe filename for markdown docs.
func toSafeMarkdownFilename(modulePath string) string {
	safeName := strings.ReplaceAll(modulePath, "/", "_")
	if safeName == "." || safeName == "" {
		safeName = "root"
	}
	return safeName + ".md"
}

func makeLogEvent(onEvent func(Event)) LogEventFunc {
	return func(t EventType, m string) {
		if onEvent != nil {
			onEvent(Event{Type: t, Message: m})
		}
	}
}
