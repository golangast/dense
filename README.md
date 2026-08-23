
<div align="center">

[![Go Version](https://img.shields.io/github/go-mod/go-version/golangast/dense?style=for-the-badge&logo=go&logoColor=white)](https://github.com/golangast/dense)
[![GoDoc](https://img.shields.io/badge/godoc-reference-007d9c.svg?style=for-the-badge&logo=go&logoColor=white)](https://pkg.go.dev/github.com/golangast/dense)
[![Go Report Card](https://goreportcard.com/badge/github.com/golangast/dense?style=for-the-badge)](https://goreportcard.com/report/github.com/golangast/dense)
[![Build Status](https://img.shields.io/badge/build-passing-brightgreen.svg?style=for-the-badge&logo=github-actions)](https://github.com/golangast/dense)
[![Status](https://img.shields.io/badge/Status-Beta-orange.svg?style=for-the-badge)](https://github.com/golangast/dense)

[![GitHub License](https://img.shields.io/github/license/golangast/dense?style=for-the-badge)](https://github.com/golangast/dense/blob/main/LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/stargazers)
[![GitHub Forks](https://img.shields.io/github/forks/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/network/members)
[![GitHub Issues](https://img.shields.io/github/issues/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/issues)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=for-the-badge)](http://makeapullrequest.com)

</div>

<br />

> **A Golang Developer focused on building Custom Natural Language Processing (NLP) solutions.**

---

## 🚀 About Me

As a versatile **Golang Developer**, I specialize in high-performance, custom-built **Natural Language Processing (NLP) models**. My commitment is to deliver robust, efficient, and novel solutions for complex structured language interpretation, with a core focus on systems like my **nlptagger** and **Dense** projects.

---

# Dense

Dense is a compact Go project for training and using a dense neural model that classifies developer prompts and turns them into safe code actions. The system combines a small MLP model, deterministic intent parsing, and Go AST-aware validation to support interactive Go editing.

## 📋 Table of Contents

- [What It Can Do](#what-it-can-do)
- [Typical Usage Patterns](#typical-usage-patterns)
- [Project Layout](#project-layout)
- [Learned Capabilities](#learned-capabilities)
- [Make Watch](#make-watch)
- [GitHub Stats & Coding Activity](#-github-stats--coding-activity)
- [General Info & Architecture](#general-info)
- [Requirements](#requirements)
- [Reference Commands](#reference-commands)

---

## What It Can Do

### 1. Train the Dense Model
The project includes a training tool that loads command examples, learns a simple dense MLP classifier, and saves the model to disk.

```bash
make train
# Or run built binary directly:
./bin/dense_train -data=data/training/command_examples.pb -model=data/models/dense/model.gob

```

* **Training over command examples**
* **Updating the model with new prompt patterns**
* **Generating reusable models for interactive editing and CI**

### 2. Run the Interactive Go Assistant

Use the Make target to build and run the interactive assistant:

```bash
make llm
# Or run built binary with flags:
./bin/dense_llm -model=data/models/dense/model.gob

```

### 3. Support Multiple Conversations

The assistant maintains multiple named conversations at once using built-in slash commands:

* `/new [name]`
* `/list`
* `/switch <name>`
* `/delete <name>`
* `/current`
* `/file <path>`
* `/help`

### 4. Target Files for Direct Editing

Each conversation can point at a Go file to update directly:

```text
/file ./internal/ai/dense/example.go

```

```bash
# Run interactive with target file:
./bin/dense_llm -model=data/models/dense/model.gob -file ./example.go

```

### 5. Generate Go Code from Intent

Infers prompt intent to create functions, methods, imports, structs, unit tests, error checks, and symbol deletions via Go AST.

### 6. Modify Go Files Safely via AST-Based Edits

Uses AST manipulation rather than string concatenation for:

* Import insertion
* Function insertion/replacement
* Type and struct insertion
* Receiver-aware method generation

### 7. Validate Generated Code Before Saving

Validates using `go/parser` and `go/types` prior to filesystem updates:

* Parse validation
* Type validation via `go/types`
* Package-scope symbol inspection
* Import safety checks

### 8. Understand Package-Level Context

Inspects surrounding workspace files to infer existing functions, types, and imported packages to avoid duplicate or conflicting declarations.

### 9. Handle Non-Go File Operations

Supports folder creation, file editing, and file creation/deletion through conversational flow.

### 10. Run One-Shot Prompt Classification

Execute single prompt actions via CLI without opening an interactive shell:

```bash
make build-dense_llm
./bin/dense_llm -model=data/models/dense/model.gob -prompt 'create function Sum(a int, b int) int'

```

---

## Typical Usage Patterns

### Train a Model

```bash
make train
./bin/dense_train -data=data/training/command_examples.pb -model=data/models/dense/model.gob

```

### Start Interactive Assistant

```bash
make llm

```

Example commands inside shell:

```text
create function ComputeSum(a int, b int) int
add import "fmt"
create method DoThing on Service(a int) error
add unit test for DoWork
create struct User
create file main.go

```

---

## Project Layout

```text
├── cmd/tools/
│   ├── dense_train/   # Training entry point
│   └── dense_llm/     # Interactive LLM-like assistant and editor
├── internal/ai/
│   ├── dense/         # Dense model, feature extraction, AST editing logic
│   └── training/      # Command example schema and corpus support
├── data/
│   ├── models/dense/  # Saved trained model artifacts
│   └── training/      # Command example datasets

```

---

## Learned Capabilities

Dense incorporates several workspace-aware features:

* **Workspace Indexing**: Builds a cross-file `WorkspaceGraph` of symbols and ASTs.
* **Intent Parsing & Routing**: Maps prompts directly to explicit AST actions (`ADD_FUNC`, `ADD_TYPE`, `REPLACE`, `INJECT_TAGS`, etc.).
* **Signature Inference**: Infers parameter/result lists from existing symbols when prompts provide short function names.
* **Webserver Scaffolding**: Instantly generates safe HTTP server scaffolds (`StartServer`).
* **Automated Diagnostics & Fixes**: Captures `go` compiler errors and applies conservative iterative fixes via `dense fix`.
* **Safety-First Workflow**: Full validation pass via Go parser and type-checker before writing to disk.

---

## Make Watch

* **Purpose**: `make watch` builds and runs `dense_watch` to monitor the workspace for file changes and apply automatic, AST-aware fixes.
* **Execution**: `make watch` (runs `./bin/dense_watch -dir=. -auto-apply`).
* **Supported Flags**:
* `-dir`: Directory to watch (default `.`)
* `-debounce`: Debounce interval (default `500ms`)
* `-auto-apply`: Automatically apply previewed fixes
* `-auto-restore-git`: Restores from `git` HEAD if verification fails
* `-poll`: Periodically runs suggestion passes



---

## 📊 GitHub Stats & Coding Activity

<div align="center">
<img src="https://github-profile-summary-cards.vercel.app/api/cards/profile-details?username=golangast&theme=radical" alt="GitHub Profile Summary" />





<img src="https://github-readme-tech-stack.vercel.app/api/cards?username=golangast&theme=radical" alt="GitHub Tech Stack" />





<a href="https://github.com/golangast">
<img src="https://github-readme-activity-graph.vercel.app/graph?username=golangast&theme=radical" alt="Zachary's GitHub Activity Graph" />
</a>





<img src="https://github-readme-stats.vercel.app/api?username=golangast&show_icons=true&theme=radical&hide_border=true" alt="Zachary's GitHub Stats" />
<img src="https://github-readme-stats.vercel.app/api/top-langs/?username=golangast&layout=compact&theme=radical&hide_border=true" alt="Top Languages" />





<img src="https://streak-stats.demolab.com/?user=golangast&theme=radical&hide_border=true" alt="GitHub Streak" />
</div>

---

## General Info

* **Technologies**: Go 1.26+, Go AST, `go/types`, `fsnotify`.
* **Requirements**: Go 1.26 or later, `git` (optional, for auto-restore features).

## Reference Commands

* `make build` — Build all workspace binaries into `bin/`
* `make train` — Build and run the trainer tool
* `make llm` — Build and launch the interactive assistant
* `make watch` — Start the workspace watcher with auto-apply enabled
EOF

```

```