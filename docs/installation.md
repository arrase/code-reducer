# Installation & Building Guide

This guide walks you through setting up Code-Reducer, building from source, utilizing pre-compiled binaries, and performing your first documentation run.

---

## System Prerequisites

Before installing Code-Reducer, ensure your system meets the following requirements:

1. **Go Runtime**: Version **1.26 or higher** (required only if building from source).
2. **Ollama Engine**: Running locally (or accessible over network) with at least one compatible open-weight model pulled:
   ```bash
   ollama pull ornith:9b
   # or
   ollama pull gemma4:26b
   ```
3. **Git**: Installed and available in system `PATH`.

---

## Building from Source

To compile Code-Reducer directly from source code:

### 1. Clone the Repository

```bash
git clone https://github.com/arrase/code-reducer.git
cd code-reducer
```

### 2. Compile the Binary

Build the binary target using `go build`:

```bash
go build -o code-reducer main.go
```

This compiles a single, statically linked binary named `code-reducer` in your current working directory.

### 3. Optional: Install to System PATH

To invoke `code-reducer` globally across any repository on your machine, copy the compiled binary to a directory included in your system `PATH`:

```bash
sudo cp code-reducer /usr/local/bin/
```

Or install directly via `go install`:

```bash
go install github.com/arrase/code-reducer@latest
```

---

## Pre-Compiled Release Binaries

If you prefer not to install the Go toolchain, pre-compiled release binaries are attached to every official release on GitHub:

1. Navigate to the GitHub Releases page: [https://github.com/arrase/code-reducer/releases](https://github.com/arrase/code-reducer/releases)
2. Download the appropriate binary archive for your OS and architecture (e.g., `code-reducer_linux_amd64.tar.gz`).
3. Extract the archive and grant executable permissions:

```bash
chmod +x code-reducer
```

---

## Quick Start Step-by-Step Walkthrough

Follow these three steps to document a repository using Code-Reducer:

### Step 1: Run Configuration Setup (`setup`)

Navigate to your target repository root and run the setup wizard:

```bash
code-reducer setup
```

The wizard will prompt you for model parameters, context limits, and ignored patterns, generating a `.code-reducer.yaml` configuration file.

### Step 2: Initialize Documentation (`init`)

Perform the initial repository scan and Map-Reduce synthesis:

```bash
code-reducer init
```

Code-Reducer will scan source files, build module briefings under `wiki/modules/`, write system blueprints (`wiki/architecture.md`, `wiki/quickstart.md`), create the metadata cache (`wiki/.metadata.json`), and generate `AGENTS.md`.

### Step 3: Keep Docs Updated (`update`)

Whenever files are added, modified, or deleted in your repository, run:

```bash
code-reducer update
```

Code-Reducer will calculate file SHA256 hashes, compare them against `.metadata.json`, and incrementally rebuild only changed files and impacted parent modules.
