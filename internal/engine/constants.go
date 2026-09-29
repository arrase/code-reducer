package engine

import "time"

const (
	defaultHTTPTimeout  = 10 * time.Minute
	maxErrorBodyBytes   = 1024
	defaultChunkOverlap = 800
	minNumCtxFloor      = 512
	metadataFileName    = ".metadata.json"
	agentsFileName      = "AGENTS.md"
	defaultDirPerm      = 0755

	markdownHeadingPrefix = "### "
	subsystemPrefix       = "Subsystem: "

	// slotLineWordBudget is the word ceiling of a line slot. The line sits under a
	// heading the code already wrote, so the budget is what keeps a rambling answer
	// from becoming an unreadable block under a fixed heading.
	slotLineWordBudget = 40

	headingResponsibility = "Responsibility"
	headingComponents     = "Components"
	headingDataFlow       = "Data Flow"
	headingErrorHandling  = "Error Handling"
)

type LogEventFunc func(EventType, string)
