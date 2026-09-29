package config

const (
	// CodeReducerModelIDEnvKey is the env key for model ID override.
	CodeReducerModelIDEnvKey = "CODE_REDUCER_MODEL_ID"
	// CodeReducerThinkEnvKey is the env key for reasoning mode override.
	CodeReducerThinkEnvKey = "CODE_REDUCER_THINK"
	// OllamaBaseURLEnvKey is the env key for Ollama URL override.
	OllamaBaseURLEnvKey = "OLLAMA_BASE_URL"
	// OllamaNumCtxEnvKey is the env key for context size override.
	OllamaNumCtxEnvKey = "OLLAMA_NUM_CTX"
	// OllamaNumPredictEnvKey is the env key for generation budget override.
	OllamaNumPredictEnvKey = "OLLAMA_NUM_PREDICT"
	// CodeReducerSlotNumPredictEnvKey is the env key for the line slot budget.
	CodeReducerSlotNumPredictEnvKey = "CODE_REDUCER_SLOT_NUM_PREDICT"
	// CodeReducerParagraphNumPredictEnvKey is the env key for the paragraph slot budget.
	CodeReducerParagraphNumPredictEnvKey = "CODE_REDUCER_PARAGRAPH_NUM_PREDICT"
	// CodeReducerIncludeTestsEnvKey is the env key for the test file switch.
	CodeReducerIncludeTestsEnvKey = "CODE_REDUCER_INCLUDE_TESTS"

	// OllamaDefaultBaseURL is the default URL for local Ollama api.
	OllamaDefaultBaseURL = "http://localhost:11434"
	// OllamaDefaultModelID is the default LLM model.
	OllamaDefaultModelID = "ornith:9b"
	// OllamaDefaultNumCtx is the default context size.
	OllamaDefaultNumCtx = 8192
	// DefaultDocsDir is the default documentation folder name.
	DefaultDocsDir = "wiki"
	// ConfigFileName is the configuration filename.
	ConfigFileName = ".code-reducer.yaml"
	configFilePerm = 0600

	// ThinkDefault disables reasoning output by default. Reasoning models spend
	// the output budget on hidden thinking, which leaves content empty or
	// hallucinated and is several times slower.
	ThinkDefault = false
	// NumPredictDefault of 0 lets Ollama decide the generation budget.
	NumPredictDefault = 0
	// IncludeTestsDefault excludes test files from discovery. Documenting a test
	// file mostly restates its assertions and costs a full extraction pass per
	// file, so tests are opt-in.
	IncludeTestsDefault = false
	// CharsPerTokenDefault is the measured characters-per-token ratio for Go and
	// Markdown payloads, used to turn the token budget into a character budget.
	CharsPerTokenDefault = 3.0
	// OutputTokenReserveDefault is the number of context tokens held back from
	// the prompt budget so the model always has room to generate an answer.
	OutputTokenReserveDefault = 1024
	// SlotNumPredictDefault bounds every document line slot. A line slot is a
	// micro-task whose answer the code trims to its first sentence and to
	// slotLineWordBudget words, so tokens past the first sentence are pure waste.
	// Measured with think disabled a one-line slot costs 34 tokens for a 164
	// character answer, so 192 is roughly five times a real one-line answer and
	// still caps a runaway generation. With thinking enabled the same slot costs
	// 400 tokens, almost all of it reasoning the tool never reads, which is why
	// this default is sized for a non-thinking answer and why a clipped line is
	// salvaged down to its first complete sentence.
	SlotNumPredictDefault = 192
	// ParagraphNumPredictDefault bounds every document paragraph slot. Measured
	// with think disabled a paragraph slot costs 63 tokens for a 287 character
	// answer, so 1024 is roughly sixteen times a real paragraph answer. With
	// thinking enabled the same slot costs 795 tokens, most of it reasoning the
	// tool never reads. A paragraph answer is not shortened by the code, and a
	// clipped one is salvaged only up to its last complete sentence, so the bound
	// is the only thing keeping a talkative paragraph from losing its tail.
	ParagraphNumPredictDefault = 1024

	// DefaultSystemPrompt is the default system instructions for the LLM.
	DefaultSystemPrompt = "You are Code-Reducer, an expert technical writer and code analyzer. Your job is to strictly follow instructions. You do not yap, you do not write filler.\n" +
		"DEFENSIVE RULES: 1. Do NOT use absolute terms ('always', 'never', 'zero') unless explicitly proven. 2. Do NOT guess downstream consequences or invent unhandled paths. If an error is swallowed, just say it is swallowed. 3. Do NOT name standard library packages unless explicitly stated in the source text. 4. Only report facts you are 100% sure about.\n"

	// DefaultModuleSynthesisPrompt is the default prose style for module page slots.
	// The page skeleton is written by the code, so this prompt must never ask the
	// model for headings, grouping or a whole page.
	DefaultModuleSynthesisPrompt = "Task: Write the prose for one slot of a module documentation page.\nRule 1: The headings, their order and the component names are already fixed by the tooling. Never emit headings, lists, code fences or any other structure.\nRule 2: Answer with the requested slot text only, dense and technical, and keep it as short as the slot instruction asks.\nRule 3: Use only the supplied facts, and say nothing about code you were not shown."

	// DefaultArchitecturePrompt is the default prose style for the architecture and
	// quickstart page slots, which have the same fixed-skeleton contract.
	DefaultArchitecturePrompt = "Task: Write the prose for one slot of a project documentation page (architecture or quickstart).\nRule 1: The title, the headings and their order are already fixed by the tooling. Never emit headings, lists, code fences or any other structure.\nRule 2: Answer with the requested slot text only, dense and developer-friendly, and keep it as short as the slot instruction asks.\nRule 3: Use only the supplied module summaries, and never invent a module, a command or a workflow you were not shown."

	// DefaultFileFactConsolidationPrompt is the default prompt for consolidation.
	DefaultFileFactConsolidationPrompt = "You are a specialized code documentation assistant.\nConsolidate, deduplicate and merge the following facts extracted from different chunks of the same file into a single, cohesive summary."
)

// ExtractionStep represents a single fact-extraction phase for files.
type ExtractionStep struct {
	Name   string `yaml:"name"`
	Prompt string `yaml:"prompt"`
}

// Config represents the schema of .code-reducer.yaml
type Config struct {
	ModelID                     string           `yaml:"model_id"`
	OllamaBaseURL               string           `yaml:"ollama_base_url"`
	OllamaNumCtx                int              `yaml:"ollama_num_ctx"`
	DocsDir                     string           `yaml:"docs_dir"`
	Think                       bool             `yaml:"think,omitempty"`
	NumPredict                  int              `yaml:"num_predict,omitempty"`
	SlotNumPredict              int              `yaml:"slot_num_predict,omitempty"`
	ParagraphNumPredict         int              `yaml:"paragraph_num_predict,omitempty"`
	CharsPerToken               float64          `yaml:"chars_per_token,omitempty"`
	OutputTokenReserve          int              `yaml:"output_token_reserve,omitempty"`
	SystemPrompt                string           `yaml:"system_prompt"`
	ModuleSynthesisPrompt       string           `yaml:"module_synthesis_prompt"`
	ArchitecturePrompt          string           `yaml:"architecture_prompt"`
	FileFactConsolidationPrompt string           `yaml:"file_fact_consolidation_prompt"`
	ExtractionSteps             []ExtractionStep `yaml:"extraction_steps"`
	IncludeTests                bool             `yaml:"include_tests,omitempty"`
	Ignore                      []string         `yaml:"ignore"`
}

// DefaultExtractionSteps is the standard list of extraction steps, optimized for language-agnostic extraction and ~10B LLMs.
var DefaultExtractionSteps = []ExtractionStep{
	{
		Name:   "API_SIGNATURES",
		Prompt: "Task: Extract the public API surface.\nOutput: A strict Markdown list of all exported or public elements (classes, functions, methods, types). Include parameters and return types. Do not explain internal execution logic.",
	},
	{
		Name:   "BUSINESS_LOGIC",
		Prompt: "Task: Extract the core purpose and domain rules.\nOutput: Explain the primary domain problem this code solves. List the high-level algorithm steps. Ignore syntax, standard library usage, and basic implementation details.",
	},
	{
		Name:   "STATE_AND_CONCURRENCY",
		Prompt: "Task: Identify mutable state and thread safety.\nOutput: List global variables, shared states, or class-level properties that are modified. Identify synchronization mechanisms (locks, mutexes, async/await, atomic types). If entirely stateless, output exactly: 'No mutable state'.",
	},
	{
		Name:   "ERRORS_AND_SIDE_EFFECTS",
		Prompt: "Task: Analyze external I/O and error propagation.\nOutput: Detail interactions with external systems (network, disk, databases, APIs). Explain how errors are propagated (exceptions, error return codes, crash/panic). If no I/O exists, state 'No external side effects'.",
	},
}
