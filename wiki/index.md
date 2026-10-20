# Documentation Index

## Overview

- [Architecture Overview](architecture.md)
- [Developer Quickstart](quickstart.md)

## Modules

- [Root (.)](modules/README.md) — Code-Reducer synthesizes structured documentation from source code through a Map-Reduce pipeline orchestrated by `internal/engine`. The s... (1 file: main.go)
- [cmd](modules/cmd/README.md) — The `cmd` package implements the CLI surface for the Code-Reducer documentation agent. It registers cobra commands (`init`, `read`, `sear... (6 files: init.go, read.go, root.go, search.go, setup.go, update.go)
- [internal](modules/internal/README.md) — Code-Reducer synthesizes structured documentation from source code through a Map-Reduce pipeline orchestrated by the `internal/engine` pa...
- [internal/config](modules/internal/config/README.md) — The `internal/config` module provides configuration loading, resolution, and persistence for the Code-Reducer tool. It supports multiple... (3 files: config.go, io.go, resolve.go)
- [internal/engine](modules/internal/engine/README.md) — The `internal/engine` package orchestrates LLM-based synthesis of code documentation through a Map-Reduce pipeline. It discovers source f... (7 files: cache.go, chunking.go, client.go, finalize.go, orchestrator.go, synthesize.go, tree.go)
- [internal/search](modules/internal/search/README.md) — Provides full-text search over markdown documentation sections using BM25 ranking. Discovers `.md` files under a docs directory, parses A... (1 file: search.go)
- [internal/security](modules/internal/security/README.md) — Provides path-traversal-safe path resolution and file-based process locking for repository operations. Path resolution validates that res... (1 file: security.go)
- [internal/tools](modules/internal/tools/README.md) — The `internal/tools` module provides three capability domains: safe filesystem operations with path-traversal protection, git repository... (3 files: file_tools.go, git_tools.go, markdown.go)
