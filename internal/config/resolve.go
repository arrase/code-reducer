package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// deduplicate removes duplicates from a slice.
func deduplicate(a []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range a {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func resolveString(yamlVal, defaultVal string) string {
	if yamlVal != "" {
		return yamlVal
	}
	return defaultVal
}

func loadBaseConfig(repoRoot string) (*Config, error) {
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to load configuration file: %w", err)
		}
		return &Config{}, nil
	}
	return cfg, nil
}

func resolveModelID(cfgVal, flagVal string) string {
	modelID := OllamaDefaultModelID
	if cfgVal != "" {
		modelID = cfgVal
	}
	if envVal := os.Getenv(CodeReducerModelIDEnvKey); envVal != "" {
		modelID = envVal
	}
	if flagVal != "" {
		modelID = flagVal
	}
	return modelID
}

func resolveBaseURL(cfgVal string) string {
	baseURL := OllamaDefaultBaseURL
	if cfgVal != "" {
		baseURL = cfgVal
	}
	if envVal := os.Getenv(OllamaBaseURLEnvKey); envVal != "" {
		baseURL = envVal
	}
	return baseURL
}

func resolveNumCtx(cfgVal int, flagVal string) (int, error) {
	numCtx := OllamaDefaultNumCtx
	if cfgVal > 0 {
		numCtx = cfgVal
	}
	if envVal := os.Getenv(OllamaNumCtxEnvKey); envVal != "" {
		n, err := strconv.Atoi(envVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for %s: %s", OllamaNumCtxEnvKey, envVal)
		}
		numCtx = n
	}
	if flagVal != "" {
		n, err := strconv.Atoi(flagVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for num-ctx flag: %s", flagVal)
		}
		numCtx = n
	}
	return numCtx, nil
}

func resolveThink(cfgVal bool, flagVal string) (bool, error) {
	think := ThinkDefault
	if cfgVal {
		think = true
	}
	if envVal := os.Getenv(CodeReducerThinkEnvKey); envVal != "" {
		b, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("invalid value for %s: %s", CodeReducerThinkEnvKey, envVal)
		}
		think = b
	}
	if flagVal != "" {
		b, err := strconv.ParseBool(flagVal)
		if err != nil {
			return false, fmt.Errorf("invalid value for think flag: %s", flagVal)
		}
		think = b
	}
	return think, nil
}

func resolveNumPredict(cfgVal int, flagVal string) (int, error) {
	numPredict := NumPredictDefault
	if cfgVal > 0 {
		numPredict = cfgVal
	}
	if envVal := os.Getenv(OllamaNumPredictEnvKey); envVal != "" {
		n, err := strconv.Atoi(envVal)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid value for %s: %s", OllamaNumPredictEnvKey, envVal)
		}
		numPredict = n
	}
	if flagVal != "" {
		n, err := strconv.Atoi(flagVal)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid value for num-predict flag: %s", flagVal)
		}
		numPredict = n
	}
	return numPredict, nil
}

// resolveSlotNumPredict picks the generation bound for a single document line
// slot. Zero is rejected on purpose: an unbounded slot can silently stop on the
// generation limit and turn a correct answer into a hard pipeline failure.
func resolveSlotNumPredict(cfgVal int, flagVal string) (int, error) {
	slotNumPredict := SlotNumPredictDefault
	if cfgVal > 0 {
		slotNumPredict = cfgVal
	}
	if envVal := os.Getenv(CodeReducerSlotNumPredictEnvKey); envVal != "" {
		n, err := strconv.Atoi(envVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for %s: %s", CodeReducerSlotNumPredictEnvKey, envVal)
		}
		slotNumPredict = n
	}
	if flagVal != "" {
		n, err := strconv.Atoi(flagVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for slot-num-predict flag: %s", flagVal)
		}
		slotNumPredict = n
	}
	return slotNumPredict, nil
}

// resolveParagraphNumPredict picks the generation bound for a single document
// paragraph slot. It follows the same precedence and the same rejection of a
// zero or negative value as resolveSlotNumPredict, because the failure it prevents
// is the same one: a bounded answer the code can salvage, rather than an unbounded
// one that stops on the generation limit and aborts the page.
func resolveParagraphNumPredict(cfgVal int, flagVal string) (int, error) {
	paragraphNumPredict := ParagraphNumPredictDefault
	if cfgVal > 0 {
		paragraphNumPredict = cfgVal
	}
	if envVal := os.Getenv(CodeReducerParagraphNumPredictEnvKey); envVal != "" {
		n, err := strconv.Atoi(envVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for %s: %s", CodeReducerParagraphNumPredictEnvKey, envVal)
		}
		paragraphNumPredict = n
	}
	if flagVal != "" {
		n, err := strconv.Atoi(flagVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for paragraph-num-predict flag: %s", flagVal)
		}
		paragraphNumPredict = n
	}
	return paragraphNumPredict, nil
}

func resolveCharsPerToken(cfgVal float64, flagVal string) (float64, error) {
	charsPerToken := CharsPerTokenDefault
	if cfgVal > 0 {
		charsPerToken = cfgVal
	}
	if flagVal != "" {
		f, err := strconv.ParseFloat(flagVal, 64)
		if err != nil || f <= 0 {
			return 0, fmt.Errorf("invalid value for chars-per-token flag: %s", flagVal)
		}
		charsPerToken = f
	}
	return charsPerToken, nil
}

func resolveOutputTokenReserve(cfgVal int, flagVal string) (int, error) {
	outputTokenReserve := OutputTokenReserveDefault
	if cfgVal > 0 {
		outputTokenReserve = cfgVal
	}
	if flagVal != "" {
		n, err := strconv.Atoi(flagVal)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid value for output-token-reserve flag: %s", flagVal)
		}
		outputTokenReserve = n
	}
	return outputTokenReserve, nil
}

// resolveIncludeTests picks the test file switch. Like resolveThink, the config
// file can only turn the option on: an absent key and an explicit false mean the
// same thing, so there is nothing to override.
func resolveIncludeTests(cfgVal bool, flagVal string) (bool, error) {
	includeTests := IncludeTestsDefault
	if cfgVal {
		includeTests = true
	}
	if envVal := os.Getenv(CodeReducerIncludeTestsEnvKey); envVal != "" {
		b, err := strconv.ParseBool(envVal)
		if err != nil {
			return false, fmt.Errorf("invalid value for %s: %s", CodeReducerIncludeTestsEnvKey, envVal)
		}
		includeTests = b
	}
	if flagVal != "" {
		b, err := strconv.ParseBool(flagVal)
		if err != nil {
			return false, fmt.Errorf("invalid value for include-tests flag: %s", flagVal)
		}
		includeTests = b
	}
	return includeTests, nil
}

// Flags holds the raw CLI flag values that take precedence over the environment and the config file.
type Flags struct {
	ModelID             string
	NumCtx              string
	Think               string
	NumPredict          string
	SlotNumPredict      string
	ParagraphNumPredict string
	CharsPerToken       string
	OutputTokenReserve  string
	IncludeTests        string
}

// ResolveConfig merges CLI overrides, environment variables, YAML config, and system defaults.
// It returns a fully resolved Config struct ready to be used by the pipeline runner and LLM client.
func ResolveConfig(repoRoot string, flags Flags) (*Config, error) {
	cfg, err := loadBaseConfig(repoRoot)
	if err != nil {
		return nil, err
	}

	numCtx, err := resolveNumCtx(cfg.OllamaNumCtx, flags.NumCtx)
	if err != nil {
		return nil, err
	}

	think, err := resolveThink(cfg.Think, flags.Think)
	if err != nil {
		return nil, err
	}

	numPredict, err := resolveNumPredict(cfg.NumPredict, flags.NumPredict)
	if err != nil {
		return nil, err
	}

	slotNumPredict, err := resolveSlotNumPredict(cfg.SlotNumPredict, flags.SlotNumPredict)
	if err != nil {
		return nil, err
	}

	paragraphNumPredict, err := resolveParagraphNumPredict(cfg.ParagraphNumPredict, flags.ParagraphNumPredict)
	if err != nil {
		return nil, err
	}

	charsPerToken, err := resolveCharsPerToken(cfg.CharsPerToken, flags.CharsPerToken)
	if err != nil {
		return nil, err
	}

	outputTokenReserve, err := resolveOutputTokenReserve(cfg.OutputTokenReserve, flags.OutputTokenReserve)
	if err != nil {
		return nil, err
	}

	includeTests, err := resolveIncludeTests(cfg.IncludeTests, flags.IncludeTests)
	if err != nil {
		return nil, err
	}

	resolvedSteps := cfg.ExtractionSteps
	if len(resolvedSteps) == 0 {
		resolvedSteps = DefaultExtractionSteps
	}

	return &Config{
		Ignore:                      deduplicate(cfg.Ignore),
		ExtractionSteps:             resolvedSteps,
		ModelID:                     resolveModelID(cfg.ModelID, flags.ModelID),
		OllamaBaseURL:               resolveBaseURL(cfg.OllamaBaseURL),
		OllamaNumCtx:                numCtx,
		DocsDir:                     resolveString(cfg.DocsDir, DefaultDocsDir),
		Think:                       think,
		NumPredict:                  numPredict,
		SlotNumPredict:              slotNumPredict,
		ParagraphNumPredict:         paragraphNumPredict,
		CharsPerToken:               charsPerToken,
		OutputTokenReserve:          outputTokenReserve,
		IncludeTests:                includeTests,
		SystemPrompt:                resolveString(cfg.SystemPrompt, DefaultSystemPrompt),
		ModuleSynthesisPrompt:       resolveString(cfg.ModuleSynthesisPrompt, DefaultModuleSynthesisPrompt),
		ArchitecturePrompt:          resolveString(cfg.ArchitecturePrompt, DefaultArchitecturePrompt),
		FileFactConsolidationPrompt: resolveString(cfg.FileFactConsolidationPrompt, DefaultFileFactConsolidationPrompt),
	}, nil
}
