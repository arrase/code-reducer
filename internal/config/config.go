package config

const (
	// CodeReducerModelIDEnvKey is the env key for model ID override.
	CodeReducerModelIDEnvKey = "CODE_REDUCER_MODEL_ID"
	// OllamaBaseURLEnvKey is the env key for Ollama URL override.
	OllamaBaseURLEnvKey = "OLLAMA_BASE_URL"
	// OllamaNumCtxEnvKey is the env key for context size override.
	OllamaNumCtxEnvKey = "OLLAMA_NUM_CTX"

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

	// DefaultSystemPrompt is the default system instructions for the LLM.
	DefaultSystemPrompt = "You are Code-Reducer, a technical code analyzer and documentation writer. Follow instructions strictly with maximum technical conciseness.\n" +
		"Rules:\n" +
		"1. Extract and report ONLY verifiable facts explicitly present in the provided source code.\n" +
		"2. Do NOT invent behaviors, speculate on unshown code, or guess downstream consequences.\n" +
		"3. Do NOT copy source code blocks or write conversational filler.\n"

	// DefaultModuleSynthesisPrompt is the default prompt for folder summaries.
	DefaultModuleSynthesisPrompt = "Task: Write a technical documentation page for a code module based on the provided list of its internal components.\n" +
		"Rule 1: Group related functions and types under appropriate Markdown headings.\n" +
		"Rule 2: Explain the responsibility of the module and its internal data flow.\n" +
		"Rule 3: Keep it highly technical, concise, and free of source code blocks."

	// DefaultArchitecturePrompt is the default prompt for global architecture synthesis.
	DefaultArchitecturePrompt = "Task: Write the global architecture document (architecture.md) based on the module summaries.\n" +
		"Rule 1: Focus exclusively on the global architecture vision, system boundaries, end-to-end data flow, and module interactions.\n" +
		"Rule 2: Keep it concise, technical, and developer-friendly."

	// DefaultQuickstartPrompt is the default prompt for developer quickstart synthesis.
	DefaultQuickstartPrompt = "Task: Write the developer quickstart guide (quickstart.md) based on the module summaries.\n" +
		"Rule 1: Focus exclusively on prerequisites, build/installation steps, available CLI commands and flags, configuration (.code-reducer.yaml and environment variables), and package structure.\n" +
		"Rule 2: Keep it concise, actionable, and strictly factual."

	// DefaultFileFactConsolidationPrompt is the default prompt for consolidation.
	DefaultFileFactConsolidationPrompt = "Task: Consolidate and deduplicate facts extracted from different chunks of the same file into a single, concise summary.\n" +
		"Rule 1: Merge duplicate items and preserve only unique, verifiable facts.\n" +
		"Rule 2: Keep bullet points to 1 line each; do NOT copy source code blocks or add long explanations.\n" +
		"Rule 3: If all inputs are 'None', output solely 'None'."
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
	QuickstartPrompt            string           `yaml:"quickstart_prompt"`
	FileFactConsolidationPrompt string           `yaml:"file_fact_consolidation_prompt"`
	ExtractionSteps             []ExtractionStep `yaml:"extraction_steps"`
	Ignore                      []string         `yaml:"ignore"`
}

// DefaultExtractionSteps is the standard list of extraction steps, optimized for language-agnostic extraction and ~10B LLMs.
var DefaultExtractionSteps = []ExtractionStep{
	{
		Name:   "API_SIGNATURES",
		Prompt: "Task: Extract the public API surface.\nOutput: Markdown bullet list of exported/public types, functions, and methods, strictly 1 line per signature with parameters and return types. Do NOT explain internal logic or copy code blocks. If none exist, output solely 'None'.",
	},
	{
		Name:   "BUSINESS_LOGIC",
		Prompt: "Task: Extract the core purpose and domain rules.\nOutput: Maximum 5 concise bullet points describing the primary domain problem and high-level logic. Ignore syntax and boilerplate. Do NOT copy code blocks. If none exist, output solely 'None'.",
	},
	{
		Name:   "STATE_AND_CONCURRENCY",
		Prompt: "Task: Identify mutable state and thread safety.\nOutput: Concise bullet list (1 line per item) of modified package/global state, struct fields, or synchronization mechanisms (locks, mutexes, channels, atomics). Do NOT copy code blocks. If stateless, output solely 'None'.",
	},
	{
		Name:   "ERRORS_AND_SIDE_EFFECTS",
		Prompt: "Task: Analyze external I/O and error propagation.\nOutput: Concise bullet list (1 line per item) of external system interactions (filesystem, network, subprocesses, environment) and error handling patterns. Do NOT copy code blocks. If none exist, output solely 'None'.",
	},
}
