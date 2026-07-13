package engine

import "time"

const (
	defaultHTTPTimeout      = 10 * time.Minute
	maxErrorBodyBytes       = 1024
	defaultChunkOverlap     = 800
	minNumCtxFloor          = 512
	contextWindowAllocRatio = 0.75
	maxCharsMultiplier      = 3
	metadataFileName        = ".metadata.json"
	agentsFileName          = "AGENTS.md"
	defaultDirPerm          = 0755
)

type LogEventFunc func(EventType, string)
