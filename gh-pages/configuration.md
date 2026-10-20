# Configuration Reference (`.code-reducer.yaml`)

Code-Reducer relies on a centralized YAML configuration file named `.code-reducer.yaml` placed in the root directory of your repository (saved with `0600` permissions). This document covers the configuration schema, four-tier resolution hierarchy, multilingual output (`language`), prompt customization, Mermaid diagram rules, Task Routing tables, extraction pipelines, and domain-specific setups like Infrastructure-as-Code (IaC).

---

## Configuration Schema Definition

Below is the complete list of configurable properties supported by `.code-reducer.yaml`:

| Property | Type | Default Value | Description |
| :--- | :--- | :--- | :--- |
| `model_id` | `string` | `ornith:9b` | Model tag loaded in your Ollama instance. |
| `ollama_base_url` | `string` | `http://localhost:11434` | Endpoint URL of the local or remote Ollama server. |
| `ollama_num_ctx` | `integer` | `8192` | Context window size in tokens allocated for inference. |
| `docs_dir` | `string` | `wiki` | Output directory where markdown documentation is generated. |
| `language` | `string` | `""` (English) | Target natural language for generated documentation prose, headings, and table descriptions (e.g., `Spanish`, `French`, `es`, `de`). |
| `system_prompt` | `string` | See defaults | System-level prompt injected into all LLM inference calls. |
| `module_synthesis_prompt` | `string` | See defaults | Prompt applied during directory-level reduction (`modules/<path>/README.md`), including Mermaid diagram rules. |
| `architecture_prompt` | `string` | See defaults | Prompt applied for global system architecture synthesis (`architecture.md`), including Mermaid diagram rules. |
| `quickstart_prompt` | `string` | See defaults | Prompt applied for developer quickstart and Task Routing table synthesis (`quickstart.md`). |
| `file_fact_consolidation_prompt` | `string` | See defaults | Prompt applied when consolidating facts across multiple chunks of the same file. |
| `extraction_steps` | `array` | 4 default steps | Array of extraction phases executed during the Map stage. |
| `ignore` | `array` | `[]` | List of file, folder, or glob patterns ignored during scanning (combined with `.gitignore` and `docs_dir`). |

### Complete Annotated `.code-reducer.yaml`

```yaml
# LLM Execution & Endpoint Settings
model_id: ornith:9b
ollama_base_url: http://localhost:11434
ollama_num_ctx: 8192
docs_dir: wiki
language: "" # Optional: e.g. "Spanish", "German", "Japanese"

# Global System Persona & Grounding Directives
system_prompt: |
  You are Code-Reducer, a technical code analyzer and documentation writer. Follow instructions strictly with maximum technical conciseness.
  Rules:
  1. Extract and report ONLY verifiable facts explicitly present in the provided source code.
  2. Do NOT invent behaviors, speculate on unshown code, or guess downstream consequences.
  3. Do NOT copy source code blocks or write conversational filler.

# Directory Module Synthesis Prompt (with Mermaid safety rules)
module_synthesis_prompt: |-
  Task: Write a technical documentation page for a code module based on the provided list of its internal components.
  Rule 1: Group related functions and types under appropriate Markdown headings.
  Rule 2: Explain the responsibility of the module and its internal data flow.
  Rule 3: When the module has non-trivial internal data or control flow, include a fenced ```mermaid diagram (flowchart TD or sequenceDiagram, max 10 nodes) following strict Mermaid label safety: quote any node label containing parentheses, brackets, colons, or semicolons (e.g., A["parse(input)"]); never use lowercase 'end' as a flowchart node ID (use 'endNode' instead); do not use raw angle brackets <T> inside labels.
  Rule 4: Keep it highly technical, concise, and free of source code blocks.

# Global Architecture Prompt (architecture.md)
architecture_prompt: |-
  Task: Write the global architecture document (architecture.md) based on the module summaries.
  Rule 1: Focus exclusively on the global architecture vision, system boundaries, end-to-end data flow, and module interactions.
  Rule 2: Include a grounded fenced ```mermaid architecture/data-flow diagram (flowchart TD or sequenceDiagram, max 10 nodes) following strict Mermaid label safety: quote any node label containing parentheses, brackets, colons, or semicolons (e.g., A["parse(input)"]); never use lowercase 'end' as a flowchart node ID (use 'endNode' instead); do not use raw angle brackets <T> inside labels.
  Rule 3: Keep it concise, technical, and developer-friendly.

# Developer Quickstart & Task Routing Prompt (quickstart.md)
quickstart_prompt: |-
  Task: Write the developer quickstart guide (quickstart.md) based on the module summaries.
  Rule 1: Focus exclusively on prerequisites, build/installation steps, available CLI commands and flags, configuration (.code-reducer.yaml and environment variables), and package structure.
  Rule 2: Include a Task Routing markdown table (| Goal / Task | Module Doc | Key Source Files |) that maps developer tasks ("I want to...") to the relative module documentation paths (modules/.../README.md) and source files provided in the project context.
  Rule 3: Keep it concise, actionable, and strictly factual.

# Multi-Chunk File Fact Consolidation Prompt
file_fact_consolidation_prompt: |-
  Task: Consolidate and deduplicate facts extracted from different chunks of the same file into a single, concise summary.
  Rule 1: Merge duplicate items and preserve only unique, verifiable facts.
  Rule 2: Keep bullet points to 1 line each; do NOT copy source code blocks or add long explanations.
  Rule 3: If all inputs are 'None', output solely 'None'.

# Map Phase Fact Extraction Pipeline
extraction_steps:
  - name: API_SIGNATURES
    prompt: |-
      Task: Extract the public API surface.
      Output: Markdown bullet list of exported/public types, functions, and methods, strictly 1 line per signature with parameters and return types. Do NOT explain internal logic or copy code blocks. If none exist, output solely 'None'.
  - name: BUSINESS_LOGIC
    prompt: |-
      Task: Extract the core purpose and domain rules.
      Output: Maximum 5 concise bullet points describing the primary domain problem and high-level logic. Ignore syntax and boilerplate. Do NOT copy code blocks. If none exist, output solely 'None'.
  - name: STATE_AND_CONCURRENCY
    prompt: |-
      Task: Identify mutable state and thread safety.
      Output: Concise bullet list (1 line per item) of modified package/global state, struct fields, or synchronization mechanisms (locks, mutexes, channels, atomics). Do NOT copy code blocks. If stateless, output solely 'None'.
  - name: ERRORS_AND_SIDE_EFFECTS
    prompt: |-
      Task: Analyze external I/O and error propagation.
      Output: Concise bullet list (1 line per item) of external system interactions (filesystem, network, subprocesses, environment) and error handling patterns. Do NOT copy code blocks. If none exist, output solely 'None'.

# Filesystem Ignore Patterns
ignore:
  - README.md
  - .code-reducer.yaml
  - go.sum
  - go.mod
  - screenshots
  - gh-pages
  - LICENSE
  - AGENTS.md
```

---

## Four-Tier Precedence Chain

Code-Reducer resolves configuration values dynamically using a deterministic four-tier precedence hierarchy (`config.ResolveConfig`):

```
┌─────────────────────────────────────────────────────────┐
│ Top Priority:  1. CLI Flags                             │
├─────────────────────────────────────────────────────────┤
│ Tier 2:        2. Environment Variables                 │
├─────────────────────────────────────────────────────────┤
│ Tier 3:        3. YAML Configuration File               │
├─────────────────────────────────────────────────────────┤
│ Fallback:      4. Hardcoded System Defaults             │
└─────────────────────────────────────────────────────────┘
```

When resolving options at runtime:

1. **CLI Flags**: Passed explicitly during subcommand invocation (`--model-id`, `--num-ctx`, `--language`).
2. **Environment Variables**: Read from the shell environment:
   - `CODE_REDUCER_MODEL_ID` overrides `model_id`.
   - `CODE_REDUCER_LANGUAGE` overrides `language`.
   - `OLLAMA_BASE_URL` overrides `ollama_base_url`.
   - `OLLAMA_NUM_CTX` overrides `ollama_num_ctx`.
3. **YAML File**: Values defined in `.code-reducer.yaml`.
4. **System Defaults**: Built-in fallbacks (`ornith:9b`, `http://localhost:11434`, `8192`, `wiki`, `""` for default English).

---

## Multilingual Documentation (`language`)

When `language` is set (via `.code-reducer.yaml`, `CODE_REDUCER_LANGUAGE`, or `--language`), `config.LanguageInstruction(lang)` appends a strict localization rule to all extraction, consolidation, module synthesis, architecture, and quickstart system prompts:

```text
Language Instruction:
- Write all documentation prose, headings, and table descriptions in <language>.
- Keep all code identifiers, function signatures, file paths, CLI commands, environment variables, and Mermaid syntax keywords unchanged.
```

Because `language` is included in `computeStepsHash`, switching languages automatically invalidates `<docs_dir>/.metadata.json` on the next `code-reducer update` and regenerates the wiki in the target language.

---

## System & Synthesis Prompts

Code-Reducer separates prompts by pipeline stage so each document type has dedicated grounding, formatting, and diagram constraints:

### `system_prompt`
Injected into every request sent to Ollama. Defines the core technical analyzer persona and strict grounding rules (report only verifiable facts from source code, never speculate on unshown code, and omit source code blocks or filler).

### `module_synthesis_prompt`
Executed during the **Reduce phase** when synthesizing directory-level pages (`<docs_dir>/modules/<path>/README.md`).
- Instructs the LLM to group related functions/types under Markdown headings and explain module responsibilities and data flow.
- **Mermaid Diagram Rules**: Directs the LLM to include a fenced ```` ```mermaid ```` diagram (`flowchart TD` or `sequenceDiagram`, max 10 nodes) when non-trivial flow exists, with strict Mermaid label safety rules (quote labels containing `():;`, never use lowercase `end` as a node ID, avoid raw `<T>` inside labels)—backed up by deterministic sanitization in `finalizeWiki`.

### `architecture_prompt`
Executed during **Step 3 (Global Architecture Synthesis)** to generate `<docs_dir>/architecture.md`.
- Focuses exclusively on global architecture vision, system boundaries, end-to-end data flow, and module interactions.
- Requires a grounded fenced ```` ```mermaid ```` architecture/data-flow diagram (`flowchart TD` or `sequenceDiagram`, max 10 nodes) adhering to the same Mermaid label safety rules.

### `quickstart_prompt`
Executed during **Step 4 (Generating Quickstart)** to generate `<docs_dir>/quickstart.md`.
- Focuses on prerequisites, build/installation steps, available CLI commands and flags, configuration (`.code-reducer.yaml` and environment variables), and package structure.
- **Task Routing Table**: Instructs the LLM to include a `| Goal / Task | Module Doc | Key Source Files |` Markdown table mapping developer tasks (`"I want to..."`) to relative module documentation paths (`modules/.../README.md`) and source files supplied in the project context.

### `file_fact_consolidation_prompt`
Invoked when a single source file exceeds context boundaries and is split into multiple overlapping chunks during the Map phase. Instructs the LLM to deduplicate and merge extracted facts into 1-line bullet points (or output `'None'` if all chunks produced `'None'`).

---

## Extraction Steps Pipeline (`extraction_steps`)

The Map stage processes source files through a sequence of extraction passes defined in `extraction_steps`.

### Default Extraction Steps

By default, Code-Reducer applies 4 specialized extraction queries to every code file:

1. **`API_SIGNATURES`**: Extracts exported/public types, functions, and methods (strictly 1 line per signature with parameters and return types).
2. **`BUSINESS_LOGIC`**: Extracts up to 5 concise bullet points describing core domain rules and high-level logic.
3. **`STATE_AND_CONCURRENCY`**: Identifies mutable package/global state, struct fields, locks, mutexes, channels, or atomics (or `'None'` if stateless).
4. **`ERRORS_AND_SIDE_EFFECTS`**: Maps external I/O (filesystem, network, subprocesses, environment) and error handling patterns.

### Cache Invalidation on Configuration or Prompt Modifications (`steps_hash`)

The engine computes a SHA256 digest (`steps_hash`) over `model_id`, `language`, `system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `quickstart_prompt`, `file_fact_consolidation_prompt`, and all `extraction_steps`, storing it in `<docs_dir>/.metadata.json`. Modifying any of these fields automatically invalidates cached facts and module summaries during `code-reducer update`, ensuring documentation stays aligned with your configuration.

---

## Ignore Pattern Matching (`go-gitignore`)

File filtering relies on `go-gitignore`, guaranteeing identical semantics to `.gitignore`.

### Pattern Evaluation Rules
- Direct paths: `vendor`, `build`
- File extensions: `*.tmp`, `*.log`
- Subtree exclusion: `.git`, `.venv`, `node_modules`
- Root vs Nested matching: `/bin` vs `bin/`

Default system exclusions (such as `.git` and the configured `docs_dir`) are combined with user-defined patterns in `.code-reducer.yaml` (`ignore` entries are automatically deduplicated) and local `.gitignore` files.

---

## Infrastructure-as-Code (IaC) & Terraform Support

Code-Reducer is not restricted to standard programming languages. By adjusting `system_prompt`, synthesis prompts, and `extraction_steps`, it can document cloud topologies and Infrastructure-as-Code repositories.

### Terraform Configuration Example

Below is a complete `.code-reducer.yaml` tailored for Terraform modules:

```yaml
model_id: "ornith:9b"
ollama_base_url: "http://localhost:11434"
ollama_num_ctx: 8192
docs_dir: "wiki"

system_prompt: |
  You are Code-Reducer, an expert Cloud Architect and Terraform infrastructure analyzer. Follow instructions strictly with maximum technical conciseness.
  Rules:
  1. Do NOT assume resources exist unless explicitly declared in the configuration.
  2. Do NOT guess downstream resource side effects unless directly linked.
  3. Only report resource definitions and configurations explicitly present in the source files.

module_synthesis_prompt: |
  Task: Write a technical documentation page for a Terraform module based on the provided list of its internal resources, variables, and sub-modules.
  Rule 1: Group related resources (networking, compute, database, IAM) under appropriate Markdown headings.
  Rule 2: Explain resource dependencies, data flow, and security controls.
  Rule 3: Include a fenced ```mermaid diagram (flowchart TD, max 10 nodes) with quoted labels when non-trivial resource dependencies exist.

architecture_prompt: |
  Task: Write the global cloud architecture overview (architecture.md) based on module summaries.
  Rule 1: Explain cloud topology, provider boundaries, and inter-module networking.
  Rule 2: Include a grounded fenced ```mermaid topology diagram (flowchart TD, max 10 nodes) with quoted node labels.

quickstart_prompt: |
  Task: Write the infrastructure deployment quickstart guide (quickstart.md) based on module summaries.
  Rule 1: Cover prerequisites, required variables, and standard Terraform workflow commands (init, plan, apply).
  Rule 2: Include a Task Routing markdown table (| Goal / Task | Module Doc | Key Source Files |) mapping infrastructure changes to module docs and .tf files.

file_fact_consolidation_prompt: |
  Consolidate and deduplicate facts extracted across chunks of Terraform files into 1-line bullet points.

extraction_steps:
  - name: "PROVISIONED_RESOURCES"
    prompt: |
      Task: Extract resources and data sources defined in this file.
      Output: Concise Markdown bullet list of resources (`resource "type" "name"`) and data sources (`data "type" "name"`). If none exist, output solely 'None'.
  - name: "VARIABLES_AND_OUTPUTS"
    prompt: |
      Task: Analyze input variables, outputs, and local variables.
      Output: List input variables (types, defaults) and outputs exported by this file. If none exist, output solely 'None'.
  - name: "MODULE_DEPENDENCIES"
    prompt: |
      Task: Analyze sub-module calls.
      Output: Identify external or internal modules (`module "name"`), source paths, and input parameters. If none exist, output solely 'None'.
  - name: "IAM_AND_SECURITY"
    prompt: |
      Task: Analyze permissions, IAM roles, and security groups.
      Output: Detail security groups, firewall rules, IAM policies, and KMS encryption keys. If none exist, output solely 'None'.

ignore:
  - ".terraform"
  - "*.tfstate"
  - "*.tfstate.backup"
  - ".terraform.lock.hcl"
```
