# Performance & VRAM Resource Allocation

Code-Reducer is designed specifically to execute locally alongside lightweight, open-weight LLMs via **Ollama**. This document breaks down VRAM memory consumption, context window budgeting strategies, embedded hardware benchmarks, and practical recommendations for consumer GPUs.

---

## VRAM Resource Footprint Analysis

Unlike heavy Python frameworks or containerized AI pipelines, Code-Reducer is written in pure Go. Memory management and process scheduling are handled directly by the OS, leaving system RAM and VRAM dedicated entirely to Ollama inference.

### Footprint Snapshot (~6.5 GB VRAM)

When processing standard codebases using a 9B parameter model (such as `ornith:9b`) configured with a **15,000 to 20,000 token context window**, Code-Reducer maintains a total GPU VRAM footprint of approximately **6.5 GB (6,476 MB)**.

![VRAM Usage](screenshots/vram_usage.png)

This low VRAM footprint enables developers to execute high-density Map-Reduce documentation synthesis on standard workstation hardware without requiring enterprise cloud GPUs (such as A100 or H100 cards).

---

## Context Window Budgeting Strategy

To prevent context truncation, out-of-memory (OOM) errors, or prompt degradation during file processing, Code-Reducer implements an automated **75% / 25% Context Budgeting Rule**.

```
┌─────────────────────────────────────────────────────────┐
│               Total Context Window (NumCtx)             │
├───────────────────────────────────┬─────────────────────┤
│ 75% Source File Payload           │ 25% Prompt & Output │
│ (~4 chars / token)                │ Reserve             │
└───────────────────────────────────┴─────────────────────┘
```

### 1. Dynamic File Chunking (Map Stage)

For source files that exceed available token budgets:

1. **Character Ratio Calculation**: Code-Reducer assumes an average of ~4 characters per token.
2. **Payload Limit**: Assigns 75% of `NumCtx` to file payload chunks. For an 8,192 token window, the chunk payload limit is:
   $$\text{Limit} = 8192 \times 0.75 \times 4 = 24,576 \text{ characters}$$
3. **Context Boundary Overlap**: Each chunk includes an **800-character overlap margin** relative to the preceding chunk, preserving variable definitions, scopes, and comment context across chunk boundaries.

### 2. Batch Consolidation (Reduce Stage)

During directory-level reduction:

- Component summaries are merged into dynamic batches capped at `NumCtx * 3` characters.
- If a single batch exceeds the character limit, Code-Reducer splits it into sub-batches and applies multi-layer recursive reduction.
- To prevent infinite reduction loops (e.g. if an LLM fails to compress input text), the engine tracks input vs output size reduction ratios and gracefully terminates recursion before memory limits are breached.

---

## Hardware Recommendations for Consumer GPUs

Code-Reducer operates efficiently across a broad range of consumer graphics cards:

| GPU Class | VRAM Size | Recommended Model | Max `ollama_num_ctx` | Expected Footprint |
| :--- | :--- | :--- | :--- | :--- |
| **Entry Level** | 8 GB | `ornith:9b` (Q4_K_M) | `8192` - `12000` | ~5.8 - 6.8 GB |
| **Mid Range** | 12 GB | `ornith:9b` / `gemma4:26b` | `16384` - `24000` | ~6.5 - 9.5 GB |
| **High End** | 16 GB - 24 GB | `gemma4:26b` | `32768`+ | ~11.0 - 18.0 GB |

### GPU Optimization Guidelines

#### 8 GB VRAM Cards (e.g., RTX 3060 8GB, RTX 4060 8GB)
- Keep `ollama_num_ctx` between `8192` and `12000`.
- Close VRAM-heavy applications (browsers with hardware acceleration, video editors) prior to running initial repository scans (`code-reducer init`).

#### 12 GB VRAM Cards (e.g., RTX 3060 12GB, RTX 4070 12GB)
- Recommended setting: `ollama_num_ctx: 20000` with `ornith:9b`.
- Delivers optimal balance between Map-Reduce batch density and inference speed.

#### 16 GB+ VRAM Cards (e.g., RTX 4080, RX 7900 XT)
- Allows leveraging larger parameter models like `gemma4:26b` with context windows up to `32768` tokens, enabling fewer reduction passes on large repositories.
