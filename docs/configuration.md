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
| `think` | `boolean` | `false` | Ask the model for reasoning output. Must stay `false` for a reasoning model such as `ornith:9b`. See [Reasoning Models](#reasoning-models-think). |
| `num_predict` | `integer` | `0` | Client-wide generation cap per LLM call. `0` lets Ollama decide. A lower value than a per-slot cap also caps that slot kind. |
| `slot_num_predict` | `integer` | `192` | Generation cap for each one-line documentation slot. See [Generation Bounds](#generation-bounds-and-hard-failures). |
| `paragraph_num_predict` | `integer` | `1024` | Generation cap for each documentation paragraph slot. See [Generation Bounds](#generation-bounds-and-hard-failures). |
| `chars_per_token` | `float` | `3.0` | Characters per token, used to convert the context budget into a prompt payload budget. |
| `output_token_reserve` | `integer` | `1024` | Context tokens held back from every prompt payload so generation always has room. |
| `include_tests` | `boolean` | `false` | Document test files as well. See [Test File Exclusion](#test-file-exclusion-include_tests). |
| `system_prompt` | `string` | See defaults | System-level prompt injected into all LLM inference calls. |
| `module_synthesis_prompt` | `string` | See defaults | Prose style applied when filling a module page slot. The page skeleton is written by the engine, so this must never ask for headings or a whole page. |
| `architecture_prompt` | `string` | See defaults | Prose style applied when filling an `architecture.md` or `quickstart.md` section. |
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

# Reasoning output. Required to be false for a reasoning model such as ornith:9b,
# which otherwise charges its hidden thinking against every generation budget: the
# same slot then costs 400 tokens instead of 34, and can return no content at all.
think: false

# Client-wide generation cap per call. 0 lets Ollama decide; a positive value also
# caps a documentation slot when it is lower than that slot kind's own cap.
num_predict: 0

# Generation caps for the documentation prose slots. The engine writes the page
# structure, so a line slot is trimmed to its first sentence by the code and only
# needs room for that sentence, while a paragraph slot keeps everything it wrote.
slot_num_predict: 192
paragraph_num_predict: 1024

# Prompt payload budgeting
chars_per_token: 3.0
output_token_reserve: 1024

# Document test files as well (default: false)
include_tests: false

# Global System Persona & Safety Directives
system_prompt: |
  You are Code-Reducer, an expert technical writer and code analyzer. Your job is to strictly follow instructions. You do not yap, you do not write filler.
  DEFENSIVE RULES: 
  1. Do NOT use absolute terms ('always', 'never', 'zero') unless explicitly proven. 
  2. Do NOT guess downstream consequences or invent unhandled paths. If an error is swallowed, just say it is swallowed. 
  3. Do NOT name standard library packages unless explicitly stated in the source text. 
  4. Only report facts you are 100% sure about.

# Directory Module Prose Style (applied to every slot of a module page)
module_synthesis_prompt: |-
  Task: Write the prose for one slot of a module documentation page.
  Rule 1: The headings, their order and the component names are already fixed by the tooling. Never emit headings, lists, code fences or any other structure.
  Rule 2: Answer with the requested slot text only, dense and technical, and keep it as short as the slot instruction asks.
  Rule 3: Use only the supplied facts, and say nothing about code you were not shown.

# System Architecture & Quickstart Prose Style (applied to every page section)
architecture_prompt: |-
  Task: Write the prose for one slot of a project documentation page (architecture or quickstart).
  Rule 1: The title, the headings and their order are already fixed by the tooling. Never emit headings, lists, code fences or any other structure.
  Rule 2: Answer with the requested slot text only, dense and developer-friendly, and keep it as short as the slot instruction asks.
  Rule 3: Use only the supplied module summaries, and never invent a module, a command or a workflow you were not shown.

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

1. **CLI Flags**: Passed explicitly during subcommand invocation (`--model-id`, `--num-ctx`, `--think`, `--num-predict`, `--slot-num-predict`, `--paragraph-num-predict`, `--chars-per-token`, `--output-token-reserve`, `--include-tests`).
2. **Environment Variables**: Read from shell context:
   - `CODE_REDUCER_MODEL_ID` overrides `model_id`.
   - `OLLAMA_BASE_URL` overrides `ollama_base_url`.
   - `OLLAMA_NUM_CTX` overrides `ollama_num_ctx`.
   - `CODE_REDUCER_THINK` overrides `think`.
   - `OLLAMA_NUM_PREDICT` overrides `num_predict`.
   - `CODE_REDUCER_SLOT_NUM_PREDICT` overrides `slot_num_predict`.
   - `CODE_REDUCER_PARAGRAPH_NUM_PREDICT` overrides `paragraph_num_predict`.
   - `CODE_REDUCER_INCLUDE_TESTS` overrides `include_tests`.
   - `chars_per_token` and `output_token_reserve` have no environment override.
3. **YAML File**: Values defined in `.code-reducer.yaml`.
4. **System Defaults**: Built-in fallbacks (`ornith:9b`, `http://localhost:11434`, `8192`, `wiki`).

---

## Generation Bounds and Hard Failures

Every LLM call is bounded and every incomplete answer is rejected rather than written
to disk, because a clipped answer that looks finished is worse than a failed run.

| Signal | Meaning | Result |
| :--- | :--- | :--- |
| `done_reason: "length"` on a fact extraction | The fact list is the source of truth for the page it feeds. | Hard failure, nothing cached. |
| `done_reason: "length"` on a line slot | The contract is one line, and the first complete sentence of the answer already satisfies it. | The first complete sentence is used. An answer with no complete sentence is a hard failure. |
| `done_reason: "length"` on a paragraph slot | The contract is a whole paragraph, and the text up to its last complete sentence is one. | The text up to the last complete sentence is used. An answer with no complete sentence is a hard failure. |
| Empty content | The model emitted only reasoning output, or formatting only. | Hard failure. Set `think: false`. |

So a run aborts only in two cases: a fact extraction call is truncated, or a slot
answer holds no complete sentence to salvage. A verbose model is no longer able to end
a run, because every clipped slot keeps its complete sentences.

`slot_num_predict` and `paragraph_num_predict` are ceilings, not targets. A
well-behaved slot returns far fewer tokens than its cap allows, so the values cost
nothing while they stay above the length of a real answer. They are split by slot kind
because the two need different amounts of room: the code trims a line slot to its first
sentence and to 40 words, so tokens past that sentence are pure waste, while a
paragraph slot keeps every sentence the model wrote. Measured with `think: false`, a
one-line slot costs 34 tokens and a paragraph slot 63, which is what the defaults of
`192` and `1024` leave an order of magnitude of headroom over. If you lower a cap enough
to clip a slot answer, the run salvages what is complete and reports only the case where
nothing usable is left, with a message naming the slot and the token counts.

## Reasoning Models (`think`)

`think` defaults to `false` and **must stay `false` for a reasoning model such as
`ornith:9b`**. With `think: true` the model spends the generation budget on hidden
thinking, which is slower and can leave the content empty, which the pipeline then
rejects. The budget effect is the larger one: the same one-line slot measures 400 tokens
with thinking enabled against 34 without it, and the same paragraph slot 795 against 63.
The reasoning tokens are charged against the same bound as the answer and are never
parsed, so they are pure loss, and the shipped `slot_num_predict` and
`paragraph_num_predict` defaults are sized on the `think: false` measurements. If a slot
is ever reported as truncated, check `think` first. `think: true` is only useful with a
model whose thinking output is worth the tokens.

---

## System & Prose Prompts

Code-Reducer allows overriding prompt templates to adapt outputs for specialized domain requirements or alternate languages.

These prompts carry **voice and content style only**. The engine owns the document structure: it renders every title, heading, heading order, component name and component order, and each LLM call fills exactly one prose slot. A prompt override must not ask the model for headings, lists or a whole page, because the engine would have to strip that structure back out again.

### `system_prompt`
Injected into every request sent to Ollama. It defines the core persona and defensive grounding constraints to prevent LLM hallucinations.

### `module_synthesis_prompt`
Applied to every prose slot of a module page (`wiki/modules/<path>/README.md`): the `Responsibility` line, one line per component, and the `Data Flow` and `Error Handling` paragraphs.

### `architecture_prompt`
Applied to every section of `wiki/architecture.md` and `wiki/quickstart.md`.

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

## Test File Exclusion (`include_tests`)

Test files are skipped during discovery unless `include_tests: true` is set. Documenting a test file mostly restates its assertions, while every discovered file costs one extraction pass per entry in `extraction_steps`, so the generated wiki spends its context budget on the architecture a reader actually needs.

Matching is by file-name convention and language-agnostic, applied to the base name of each discovered file:

| Convention | Examples |
| :--- | :--- |
| Go | `client_test.go` |
| Python | `client_test.py`, `test_client.py` |
| JavaScript / TypeScript | `client_test.js`, `client.test.js`, `client.spec.js`, `client.test.ts`, `client.spec.ts` |
| Ruby | `client_spec.rb`, `test_client.rb` |
| Rust | `client_test.rs` |
| JVM / .NET | `ClientTest.java`, `ClientTests.cs`, `ClientTests.scala` |

Matching is case-sensitive, so a source file that merely ends in a similar word (`contest.go`, `latest.java`) is still documented.

The `ignore` list is unaffected: listing a test pattern such as `**/*_test.go` keeps working and excludes the same files even when `include_tests: true`.

Toggling the setting is detected by `code-reducer update` like any other file-set change: newly included test files are treated as added, and newly excluded ones as deleted, which prunes their cache entries and regenerates the affected module pages.

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
  Task: Write the prose for one slot of a Terraform module documentation page.
  Rule 1: The headings, their order and the resource names are already fixed by the tooling. Never emit headings, lists, code fences or any other structure.
  Rule 2: Cover resource dependencies, data flow and security controls, as densely as the slot instruction allows.
  Rule 3: Only report resources and variables that appear in the supplied facts.

architecture_prompt: |
  Task: Write the prose for one slot of a cloud architecture or quickstart page.
  Rule 1: The title, the headings and their order are already fixed by the tooling. Never emit headings, lists, code fences or any other structure.
  Rule 2: Cover cloud topology, provider requirements, inter-module networking and the standard Terraform deployment commands, as the slot instruction allows.
  Rule 3: Never invent a resource, provider or command you were not shown.

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
