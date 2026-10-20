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

## Context Window Budgeting Strategy (`calculateFileLimit`)

To prevent context truncation, out-of-memory (OOM) errors, or prompt degradation across diverse tokenizers and programming languages, Code-Reducer implements a unified **Overhead-Aware Rune Budgeting Formula** across both Map and Reduce phases:

```text
┌─────────────────────────────────────────────────────────┐
│        Total Rune Budget (NumCtx × 2 Runes)             │
├───────────────────────────────────┬─────────────────────┤
│ Source / Batch Payload Limit      │ Exact Prompt        │
│ (Min Floor: 1,600 Runes)          │ Overhead Deduction  │
└───────────────────────────────────┴─────────────────────┘
```

$$\text{Limit} = \max\left(1600,\; \text{NumCtx} \times 2 - \text{runeCount}(\text{promptOverhead})\right)$$

### 1. Dynamic File Chunking (Map Stage)

For source files that exceed available token budgets:

1. **Conservative Rune Multiplier (`maxCharsMultiplier = 2`)**: Code-Reducer budgets 2 UTF-8 runes per context token to leave ample room for dense code punctuation, multi-language tokens, and LLM completion output.
2. **Exact Overhead Deduction**: Subtracts `maxExtractionOverhead` (the combined UTF-8 rune count of `system_prompt`, `LanguageInstruction`, the longest extraction step wrapper, target file path, and sibling directory file list) from `NumCtx * 2`, with a minimum floor of `1600` runes (`defaultChunkOverlap * 2`).
3. **Newline-Aware Overlap (`defaultChunkOverlap = 800`)**: Each chunk includes an **800-rune overlap margin** (capped at `fileLimit / 4` for small limits) and automatically snaps chunk boundaries to newline characters within the final quarter of the chunk window, preserving function signatures and comment blocks across boundaries.

### 2. Batch Consolidation (Reduce Stage)

During file fact consolidation (`reduceFileFacts`), directory synthesis (`reduceInChunks`), and global document generation (`generateDoc`):

- Component summaries are merged into dynamic batches capped by the same `calculateFileLimit(numCtx, promptOverhead)` formula via `reduceWithLLM`.
- Any single component exceeding `maxChars` is pre-split by `expandOversizedItems` into `maxChars / 2` sub-chunks with `maxChars / 10` overlap.
- If multiple batches exist, `reduceItems` recursively condenses intermediate batches and applies a **95% shrinkage guard**: if total output runes are $\ge 95\%$ of total input runes, recursion stops and intermediate summaries are concatenated directly.

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
