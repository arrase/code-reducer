---
hide:
  - toc
---

<div class="hero-section">
  <div class="hero-eyebrow">
    <i class="fa-solid fa-file-code" aria-hidden="true"></i>
    <span>Hierarchical Map-Reduce Wiki Generator</span>
  </div>
  <h1>Code Reducer</h1>
  <p class="hero-subtitle">
    A lightweight, high-performance command-line tool written in Go that automatically generates developer-friendly documentation wikis for extensive repositories using local LLMs via Ollama.
  </p>
  <div class="hero-badges">
    <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=flat&logo=go" alt="Go Version"></a>
    <a href="https://ollama.com/"><img src="https://img.shields.io/badge/Ollama-Local_Inference-black?style=flat&logo=ollama" alt="Ollama Support"></a>
    <a href="#"><img src="https://img.shields.io/badge/LLM-100%25_Private-purple?style=flat" alt="100% Private"></a>
    <a href="https://opensource.org/licenses/MIT"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT"></a>
  </div>
</div>

<div class="section-header">
  <h2>Key Features</h2>
  <span>Recursive synthesis &amp; incremental caching</span>
</div>

<div class="projects-grid">
  <div class="feature-card">
    <i class="fa-solid fa-sitemap feature-icon" aria-hidden="true"></i>
    <h3>Hierarchical Map-Reduce Pipeline</h3>
    <p>Recursively breaks codebase synthesis into structured Map and Reduce phases to document arbitrary repo sizes within strict context limits.</p>
  </div>
  <div class="feature-card">
    <i class="fa-solid fa-user-shield feature-icon" aria-hidden="true"></i>
    <h3>Private & Local LLMs</h3>
    <p>Zero cloud dependencies or API costs. Leverages local Ollama instances (e.g. <code>ornith:9b</code>, <code>gemma4:26b</code>) to keep code private.</p>
  </div>
  <div class="feature-card">
    <i class="fa-solid fa-sliders feature-icon" aria-hidden="true"></i>
    <h3>Fully Customizable Prompting</h3>
    <p>Tailor extraction steps, system prompts, module synthesis blueprints, and file fact consolidation rules directly via <code>.code-reducer.yaml</code>.</p>
  </div>
  <div class="feature-card">
    <i class="fa-solid fa-lock feature-icon" aria-hidden="true"></i>
    <h3>Enterprise Security Sandbox</h3>
    <p>Hardened filesystem guards featuring bottom-up symlink traversal checks, atomic process locking, and TOCTOU-safe atomic file writing.</p>
  </div>
  <div class="feature-card">
    <i class="fa-solid fa-bolt feature-icon" aria-hidden="true"></i>
    <h3>Fast Incremental Updates</h3>
    <p>Uses SHA256 file hashing against <code>.metadata.json</code> to only re-process changed files and propagate updates bottom-up through the module tree.</p>
  </div>
  <div class="feature-card">
    <i class="fa-solid fa-rotate feature-icon" aria-hidden="true"></i>
    <h3>Cache Invalidation</h3>
    <p>Automatically tracks extraction pipeline schema changes via <code>steps_hash</code> to safely force full documentation regeneration when prompts evolve.</p>
  </div>
</div>

<div class="section-header">
  <h2>Resource Efficiency &amp; VRAM Footprint</h2>
  <span>Optimized for local consumer GPUs</span>
</div>

Designed specifically for developer workstations and local GPU acceleration, **Code-Reducer** operates efficiently within consumer hardware constraints. Operating with a 15,000 token context window (`15K`) under Ollama, VRAM consumption remains stable at approximately **6.5 GB**:

![VRAM Usage](screenshots/vram_usage.png)

<div class="section-header">
  <h2>Documentation</h2>
  <span>Architecture, caching &amp; CLI reference</span>
</div>

Explore the documentation guides below to learn more about Code-Reducer's design, configuration, and workflows:

<div class="projects-grid">
  <a href="architecture/" class="feature-card">
    <div class="card-top">
      <i class="fa-solid fa-cubes feature-icon" aria-hidden="true"></i>
      <span class="card-badge">Read →</span>
    </div>
    <h3>Architecture & Map-Reduce Engine</h3>
    <p>Deep dive into node prefix trees (<code>DirNode</code>), dynamic file chunking, sub-batch reduction, and global synthesis phases.</p>
  </a>
  <a href="map-reduce-caching/" class="feature-card">
    <div class="card-top">
      <i class="fa-solid fa-database feature-icon" aria-hidden="true"></i>
      <span class="card-badge">Read →</span>
    </div>
    <h3>Incremental Updates & Caching</h3>
    <p>Learn how SHA256 state tracking, bottom-up change propagation, and <code>steps_hash</code> invalidation minimize LLM calls.</p>
  </a>
  <a href="security-concurrency/" class="feature-card">
    <div class="card-top">
      <i class="fa-solid fa-shield-halved feature-icon" aria-hidden="true"></i>
      <span class="card-badge">Read →</span>
    </div>
    <h3>Security Sandbox & Concurrency</h3>
    <p>Understand <code>SafeResolve</code> traversal guards, atomic process locking with <code>O_EXCL</code>, and TOCTOU symlink protection.</p>
  </a>
  <a href="configuration/" class="feature-card">
    <div class="card-top">
      <i class="fa-solid fa-gears feature-icon" aria-hidden="true"></i>
      <span class="card-badge">Read →</span>
    </div>
    <h3>Configuration & Prompts</h3>
    <p>Complete reference for <code>.code-reducer.yaml</code>, environment variable overrides, ignore filters, and extraction pipelines.</p>
  </a>
  <a href="cli-wizard/" class="feature-card">
    <div class="card-top">
      <i class="fa-solid fa-terminal feature-icon" aria-hidden="true"></i>
      <span class="card-badge">Read →</span>
    </div>
    <h3>CLI Commands & Setup Wizard</h3>
    <p>Guide to <code>setup</code>, <code>init</code>, and <code>update</code> commands, interactive prompts, and terminal execution workflows.</p>
  </a>
  <a href="performance-vram/" class="feature-card">
    <div class="card-top">
      <i class="fa-solid fa-microchip feature-icon" aria-hidden="true"></i>
      <span class="card-badge">Read →</span>
    </div>
    <h3>Performance & VRAM Usage</h3>
    <p>VRAM benchmarking, context window budgeting metrics, multi-pass reduction throughput, and hardware recommendations.</p>
  </a>
  <a href="installation/" class="feature-card">
    <div class="card-top">
      <i class="fa-solid fa-download feature-icon" aria-hidden="true"></i>
      <span class="card-badge">Read →</span>
    </div>
    <h3>Installation & Building</h3>
    <p>Step-by-step instructions for building Code-Reducer from source using Go 1.26+, binary installation, and Ollama integration.</p>
  </a>
</div>
