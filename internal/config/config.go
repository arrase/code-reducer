package config

const (
	// CodeReducerModelIDEnvKey is the env key for model ID override.
	CodeReducerModelIDEnvKey = "CODE_REDUCER_MODEL_ID"
	// OllamaBaseURLEnvKey is the env key for Ollama URL override.
	OllamaBaseURLEnvKey      = "OLLAMA_BASE_URL"
	// OllamaNumCtxEnvKey is the env key for context size override.
	OllamaNumCtxEnvKey       = "OLLAMA_NUM_CTX"

	// OllamaDefaultBaseURL is the default URL for local Ollama api.
	OllamaDefaultBaseURL = "http://localhost:11434"
	// OllamaDefaultModelID is the default LLM model.
	OllamaDefaultModelID = "ornith:9b"
	// OllamaDefaultNumCtx is the default context size.
	OllamaDefaultNumCtx  = 8192
	// DefaultDocsDir is the default documentation folder name.
	DefaultDocsDir       = "wiki"
	// ConfigFileName is the configuration filename.
	ConfigFileName       = ".code-reducer.yaml"
	configFilePerm       = 0600

	// DefaultSystemPrompt is the default system instructions for the LLM.
	DefaultSystemPrompt = "You are Code-Reducer, an expert technical writer and code analyzer. Your job is to strictly follow instructions. You do not yap, you do not write filler.\n" +
		"DEFENSIVE RULES: 1. Do NOT use absolute terms ('always', 'never', 'zero') unless explicitly proven. 2. Do NOT guess downstream consequences or invent unhandled paths. If an error is swallowed, just say it is swallowed. 3. Do NOT name standard library packages unless explicitly stated in the source text. 4. Only report facts you are 100% sure about.\n"

	// DefaultModuleSynthesisPrompt is the default prompt for folder summaries.
	DefaultModuleSynthesisPrompt = "Task: Write a technical documentation page for a code module based on the provided list of its internal components.\nRule 1: Group related functions and classes under appropriate Markdown headings.\nRule 2: Explain the responsibility of the module and the data flow.\nRule 3: Keep it highly technical and dense."

	// DefaultArchitecturePrompt is the default prompt for architecture synthesis.
	DefaultArchitecturePrompt = "Task: Write a global architecture or quickstart document based on the module summaries.\nRule 1: Explain the system boundaries and how the modules interact.\nRule 2: Provide a dense, developer-friendly overview."

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
	SystemPrompt                string           `yaml:"system_prompt"`
	ModuleSynthesisPrompt       string           `yaml:"module_synthesis_prompt"`
	ArchitecturePrompt          string           `yaml:"architecture_prompt"`
	FileFactConsolidationPrompt string           `yaml:"file_fact_consolidation_prompt"`
	ExtractionSteps             []ExtractionStep `yaml:"extraction_steps"`
	Ignore                      []string         `yaml:"ignore"`
}

// DefaultExtractionSteps is the standard list of extraction steps.
var DefaultExtractionSteps = []ExtractionStep{
	{
		Name:   "API_SIGNATURES",
		Prompt: "Task: Extract the public surface area of the file.\nOutput: A strict Markdown list of all exported structs, interfaces, and methods. For each, note the actual input and output types. Ignore internal logic.",
	},
	{
		Name:   "BUSINESS_LOGIC",
		Prompt: "Task: Analyze the business logic and domain concepts.\nOutput: Explain what business rules or domain concepts this file solves. Describe the high-level algorithmic flow. Ignore implementation details.",
	},
	{
		Name:   "STATE_AND_CONCURRENCY",
		Prompt: "Task: Analyze state mutation and concurrency.\nOutput: List all mutable state (global variables, changing struct fields) and what concurrency mechanisms (e.g., sync.Mutex, channels) protect them. If none, state 'No mutable state'.",
	},
	{
		Name:   "ERRORS_AND_SIDE_EFFECTS",
		Prompt: "Task: Analyze side effects and error handling.\nOutput: Detail how this code communicates with the outside world (I/O like network, disk, DB) and how it handles/returns errors (wrap, sentinel, panic).",
	},
}
