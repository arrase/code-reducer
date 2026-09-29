package engine

import "github.com/arrase/code-reducer/internal/config"

// promptTokenBudget returns the number of context tokens available to a prompt
// payload, holding outputReserve tokens back so generation always has headroom.
func promptTokenBudget(numCtx, outputReserve int) int {
	if numCtx < minNumCtxFloor {
		numCtx = minNumCtxFloor
	}
	if outputReserve < 0 {
		outputReserve = 0
	}
	if outputReserve >= numCtx {
		outputReserve = numCtx / 4
	}
	return numCtx - outputReserve
}

// promptCharBudget converts the prompt token budget into a character budget using
// the measured characters-per-token ratio for the payload being sent.
func promptCharBudget(numCtx, outputReserve int, charsPerToken float64) int {
	if charsPerToken <= 0 {
		charsPerToken = config.CharsPerTokenDefault
	}
	return int(float64(promptTokenBudget(numCtx, outputReserve)) * charsPerToken)
}
