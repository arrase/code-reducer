# Configuration Reference (`.code-reducer.yaml`)

Code-Reducer relies on a centralized YAML configuration file named `.code-reducer.yaml` placed in the root directory of your repository. This document covers the configuration schema, resolution hierarchy, prompt customization, extraction pipeline, ignore rules, and domain-specific setups like Infrastructure-as-Code (IaC).

---

## Configuration Schema Definition

Below is the complete list of configurable properties supported by `.code-reducer.yaml`:

| Property | Type | Default Value | Description |
| :--- | :--- | :--- | :--- |
| `model_id` | `string` | `ornith:9b` | Model tag loaded in your Ollama instance. |
| `ollama_base_url` | `string` | `http://localhost:11434` | Endpoint URL of the local or remote Ollama server. |
| `ollama_num_ctx` | `integer` | `8192` | Context window size in tokens allocated for inference. |
| `docs_dir` | `string` | `wiki` | Output directory where markdown documentation is generated. |
| `system_prompt` | `string` | See defaults | System-level prompt injected into all LLM inference calls. |
| `module_synthesis_prompt` | `string` | See defaults | Prompt applied during directory-level reduction. |
| `architecture_prompt` | `string` | See defaults | Prompt applied for global system architecture synthesis. |
| `file_fact_consolidation_prompt` | `string` | See defaults | Prompt applied when consolidating facts across file chunks. |
| `extraction_steps` | `array` | 4 default steps | Array of extraction phases executed during the Map stage. |
| `ignore` | `array` | `[]` | List of file, folder, or glob patterns ignored during scanning. |

### Complete Annotated `.code-reducer.yaml`

```yaml
# LLM Execution & Endpoint Settings
model_id: ornith:9b
ollama_base_url: http://localhost:11434
ollama_num_ctx: 20000
docs_dir: wiki

# Global System Persona & Safety Directives
system_prompt: |
  You are Code-Reducer, an expert technical writer and code analyzer. Your job is to strictly follow instructions. You do not yap, you do not write filler.
  DEFENSIVE RULES: 
  1. Do NOT use absolute terms ('always', 'never', 'zero') unless explicitly proven. 
  2. Do NOT guess downstream consequences or invent unhandled paths. If an error is swallowed, just say it is swallowed. 
  3. Do NOT name standard library packages unless explicitly stated in the source text. 
  4. Only report facts you are 100% sure about.

# Directory Module Synthesis Prompt
module_synthesis_prompt: |-
  Task: Write a technical documentation page for a code module based on the provided list of its internal components.
  Rule 1: Group related functions and classes under appropriate Markdown headings.
  Rule 2: Explain the responsibility of the module and the data flow.
  Rule 3: Keep it highly technical and dense.

# System Architecture & Quickstart Prompt
architecture_prompt: |-
  Task: Write a global architecture or quickstart document based on the module summaries.
  Rule 1: Explain the system boundaries and how the modules interact.
  Rule 2: Provide a dense, developer-friendly overview.

# Multi-chunk File Fact Consolidation Prompt
file_fact_consolidation_prompt: |-
  You are a specialized code documentation assistant.
  Consolidate, deduplicate and merge the following facts extracted from different chunks of the same file into a single, cohesive summary.

# Map Phase Fact Extraction Pipeline
extraction_steps:
  - name: API_SIGNATURES
    prompt: |-
      Task: Extract the public API surface.
      Output: A strict Markdown list of all exported or public elements (classes, functions, methods, types). Include parameters and return types. Do not explain internal execution logic.
  - name: BUSINESS_LOGIC
    prompt: |-
      Task: Extract the core purpose and domain rules.
      Output: Explain the primary domain problem this code solves. List the high-level algorithm steps. Ignore syntax, standard library usage, and basic implementation details.
  - name: STATE_AND_CONCURRENCY
    prompt: |-
      Task: Identify mutable state and thread safety.
      Output: List global variables, shared states, or class-level properties that are modified. Identify synchronization mechanisms (locks, mutexes, async/await, atomic types). If entirely stateless, output exactly: 'No mutable state'.
  - name: ERRORS_AND_SIDE_EFFECTS
    prompt: |-
      Task: Analyze external I/O and error propagation.
      Output: Detail interactions with external systems (network, disk, databases, APIs). Explain how errors are propagated (exceptions, error return codes, crash/panic). If no I/O exists, state 'No external side effects'.

# Filesystem Ignore Patterns
ignore:
  - README.md
  - .code-reducer.yaml
  - go.sum
  - go.mod
  - screenshots
  - examples
  - docs
  - LICENSE
  - AGENTS.md
```

---

## Four-Tier Precedence Chain

Code-Reducer determines parameter values dynamically using a deterministic four-tier precedence hierarchy:

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

1. **CLI Flags**: Passed explicitly during subcommand invocation (`--model-id`, `--num-ctx`).
2. **Environment Variables**: Read from shell context:
   - `CODE_REDUCER_MODEL_ID` overrides `model_id`.
   - `OLLAMA_BASE_URL` overrides `ollama_base_url`.
   - `OLLAMA_NUM_CTX` overrides `ollama_num_ctx`.
3. **YAML File**: Values defined in `.code-reducer.yaml`.
4. **System Defaults**: Built-in fallbacks (`ornith:9b`, `http://localhost:11434`, `8192`, `wiki`).

---

## System & Synthesis Prompts

Code-Reducer allows overriding prompt templates to adapt outputs for specialized domain requirements or alternate languages.

### `system_prompt`
Injected into every request sent to Ollama. It defines the core persona and defensive grounding constraints to prevent LLM hallucinations.

### `module_synthesis_prompt`
Executed during the **Reduce phase** when synthesizing folder-level briefings (`wiki/modules/<path>/README.md`). Guides how internal file facts are structured into cohesive module documentation.

### `architecture_prompt`
Executed during the final **Global Reduction phase** to produce root-level system summaries (`wiki/architecture.md` and `wiki/quickstart.md`).

### `file_fact_consolidation_prompt`
Invoked when a single source file exceeds context boundaries and is split into multiple overlapping chunks during the Map phase. It instructs the LLM to deduplicate and unify facts extracted across chunks.

---

## Extraction Steps Pipeline (`extraction_steps`)

The Map stage processes source files through a sequence of extraction passes defined in `extraction_steps`.

### Default Extraction Steps

By default, Code-Reducer applies 4 specialized extraction queries to every code file:

1. **`API_SIGNATURES`**: Extracts public types, functions, methods, parameters, and signatures.
2. **`BUSINESS_LOGIC`**: Extracts high-level algorithmic rules and core domain responsibilities.
3. **`STATE_AND_CONCURRENCY`**: Identifies shared mutable state, mutexes, locks, atomic operations, or confirms statelessness.
4. **`ERRORS_AND_SIDE_EFFECTS`**: Maps file I/O, network activity, database access, and exception/error propagation strategies.

### Cache Invalidation on Step Modifications

The engine calculates a SHA256 signature (`steps_hash`) of the entire `extraction_steps` configuration array and stores it in `<docs_dir>/.metadata.json`. If you edit, add, or remove steps in `.code-reducer.yaml`, Code-Reducer automatically invalidates cached file facts during `code-reducer update`, ensuring documentation stays aligned with your new pipeline instructions.

---

## Ignore Pattern Matching (`go-gitignore`)

File filtering relies on `go-gitignore`, guaranteeing identical semantics to `.gitignore`.

### Pattern Evaluation Rules
- Direct paths: `vendor`, `build`
- File extensions: `*.tmp`, `*.log`
- Subtree exclusion: `.git`, `.venv`, `node_modules`
- Root vs Nested matching: `/bin` vs `bin/`

Default system exclusions (like `.git`) are combined with user-defined patterns in `.code-reducer.yaml` and local project `.gitignore` files.

---

## Infrastructure-as-Code (IaC) & Terraform Support

Code-Reducer is not restricted to standard programming languages. By adjusting `system_prompt` and `extraction_steps`, it can document cloud topologies and Infrastructure-as-Code repositories.

### Terraform Configuration Example

Below is a complete `.code-reducer.yaml` tailored for Terraform modules:

```yaml
model_id: "ornith:9b"
ollama_base_url: "http://localhost:11434"
ollama_num_ctx: 8192
docs_dir: "wiki"

system_prompt: |
  You are Code-Reducer, an expert Cloud Architect and Terraform infrastructure analyzer. Your job is to strictly follow instructions. You do not yap, you do not write filler.
  DEFENSIVE RULES: 
  1. Do NOT assume resources exist unless explicitly declared in the configuration.
  2. Do NOT guess downstream resource side effects unless directly linked.
  3. Only report resource definitions and configurations you are 100% sure about.

module_synthesis_prompt: |
  Task: Write a technical documentation page for a Terraform module based on the provided list of its internal resources, variables, and sub-modules.
  Rule 1: Group related resources (networking, compute, database, IAM) under appropriate Markdown headings.
  Rule 2: Explain resource dependencies, data flow, and security controls.

architecture_prompt: |
  Task: Write a global cloud architecture overview and deployment guide based on module summaries.
  Rule 1: Explain cloud topology, provider requirements, and inter-module networking.
  Rule 2: Provide standard Terraform deployment commands (init, plan, apply).

file_fact_consolidation_prompt: |
  You are a specialized infrastructure documentation assistant. Consolidate and merge facts extracted across chunks of Terraform files.

extraction_steps:
  - name: "PROVISIONED_RESOURCES"
    prompt: |
      Task: Extract resources and data sources defined in this file.
      Output: Strict Markdown list of resources (`resource "type" "name"`) and data sources (`data "type" "name"`).
  - name: "VARIABLES_AND_OUTPUTS"
    prompt: |
      Task: Analyze input variables, outputs, and local variables.
      Output: List input variables (types, defaults) and outputs exported by this file.
  - name: "MODULE_DEPENDENCIES"
    prompt: |
      Task: Analyze sub-module calls.
      Output: Identify external or internal modules (`module "name"`), source paths, and input parameters.
  - name: "IAM_AND_SECURITY"
    prompt: |
      Task: Analyze permissions, IAM roles, and security groups.
      Output: Detail security groups, firewall rules, IAM policies, and KMS encryption keys.

ignore:
  - ".terraform"
  - "*.tfstate"
  - "*.tfstate.backup"
  - ".terraform.lock.hcl"
```
