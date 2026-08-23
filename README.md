
<div align="center">

[![Go Version](https://img.shields.io/github/go-mod/go-version/golangast/dense?style=for-the-badge&logo=go&logoColor=white&color=00ADD8)](https://golang.org)
[![GoDoc](https://img.shields.io/badge/godoc-reference-007d9c.svg?style=for-the-badge&logo=go&logoColor=white)](https://pkg.go.dev/github.com/golangast/dense)
[![Go Report Card](https://goreportcard.com/badge/github.com/golangast/dense?style=for-the-badge)](https://goreportcard.com/report/github.com/golangast/dense)

[![Go Test](https://img.shields.io/github/actions/workflow/status/golangast/dense/test.yml?branch=main&label=go%20test&style=for-the-badge&logo=go&logoColor=white)](https://github.com/golangast/dense/actions)
[![Build Status](https://img.shields.io/badge/build-passing-brightgreen.svg?style=for-the-badge&logo=github-actions&logoColor=white)](https://github.com/golangast/dense/actions)
[![Code Coverage](https://img.shields.io/badge/coverage-85%25-brightgreen.svg?style=for-the-badge&logo=codecov&logoColor=white)](https://github.com/golangast/dense)
[![CodeQL Security](https://img.shields.io/badge/CodeQL-passing-blue.svg?style=for-the-badge&logo=github&logoColor=white)](https://github.com/golangast/dense/security/code-scanning)

[![Latest Release](https://img.shields.io/github/v/release/golangast/dense?style=for-the-badge&logo=github&color=blue)](https://github.com/golangast/dense/releases)
[![GitHub License](https://img.shields.io/github/license/golangast/dense?style=for-the-badge&color=blue)](https://github.com/golangast/dense/blob/main/LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/stargazers)
[![GitHub Forks](https://img.shields.io/github/forks/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/network/members)
[![GitHub Issues](https://img.shields.io/github/issues/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/issues)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=for-the-badge)](http://makeapullrequest.com)

</div>

<br />

> **AST-Aware Natural Language Code Intelligence and Interactive Refactoring Engine for Go.**

---

## ⚡ What is Dense?

**Dense** is an AST-aware neural code engine written in Go. Instead of relying on raw string concatenation or heavy external LLM APIs, Dense combines a lightweight feed-forward neural network (MLP), deterministic intent classification, and Go AST indexers to transform natural language developer prompts into precise, syntactically valid Go code modifications.

It acts as both an interactive assistant and an active workspace daemon, executing type-checked code generation, automated refactoring, and self-healing diagnostic fixes.

### System Workflow


```

+--------------------------+
|  Natural Language Prompt |  ("add method ProcessOrder on OrderService")
+--------------------------+
|
v
+--------------------------+
|  MLP Intent Classifier   |  (Determines Action: ADD_METHOD, Target: OrderService)
+--------------------------+
|
v
+--------------------------+
|   Workspace AST Indexer  |  (Infers missing signature types & symbol context)
+--------------------------+
|
v
+--------------------------+
|   Go AST Transformation  |  (Directly manipulates Go Abstract Syntax Tree)
+--------------------------+
|
v
+--------------------------+
| Self-Healing Watch Daemon|  (Compiles & auto-fixes diagnostic errors)
+--------------------------+

                  ┌─────────────────────────────────────────┐
                  │          NATURAL LANGUAGE PROMPT        │
                  │  "add method ProcessOrder on Service"   │
                  └────────────────────┬────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                          LOCAL DENSE ENGINE                                 │
│                                                                             │
│   ┌────────────────────────┐                   ┌────────────────────────┐   │
│   │1. MLP Intent Classifier|                   │   2. Go AST Engine     │   │
│   │   Determines action &  ├──────────────────►│   Applies type-safe    │   │
│   │   target symbol        │                   │   code modification    │   │
│   └────────────────────────┘                   └────────────────────────┘   │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼
                  ┌─────────────────────────────────────────┐
                  │          SELF-HEALING WORKSPACE         │
                  │  Auto-detects & fixes compiler errors   │
                  └─────────────────────────────────────────┘

```

---

## 🔥 Key Capabilities

### 🧠 Natural Language to AST Transformation
- **Intent Parsing**: Classifies prompts into concrete AST actions (`ADD_FUNC`, `ADD_TYPE`, `REPLACE`, `INJECT_TAGS`, `ADD_IMPORT`).
- **Signature Inference**: Automatically infers missing parameter and return types by referencing workspace symbols.
- **Receiver-Aware Methods**: Generates methods bound directly to target receiver structs.
- **AST Safety Guarantee**: All code additions are inserted directly into the Go Abstract Syntax Tree (AST) to ensure structural integrity.

### 🛠 Active Workspace Monitoring & Healing (`make watch`)
- **Real-time File Supervision**: Watches project files using `fsnotify` and evaluates changes instantly.
- **Automated Compiler Diagnostics & Fixes**: Intercepts `go` compiler errors (missing braces, broken imports, missing commas) and iteratively applies conservative fixes.
- **Git Rollback Guard**: Supports optional auto-restoration from `git HEAD` if auto-applied code changes fail verification.


```

+------------------+      Changes      +-------------------+
|  Source Files    | ----------------> |  fsnotify Watcher |
+------------------+                   +-------------------+
^                                       |
| Auto-Fix                              v
+------------------+  Compiler Error   +-------------------+
| Git Safety Guard | <---------------- |  `go build` Test  |
+------------------+                   +-------------------+

```

### 💬 Interactive Shell & Multi-Session CLI
- **Interactive Multi-Session Assistant**: Manage concurrent conversation threads, attach individual target files, and execute refactorings on the fly.
- **One-Shot Execution**: Run single prompt commands directly from standard CLI arguments.
- **Workspace Navigation**: Manage project structure (creating/deleting directories and files) seamlessly through prompt interactions.

### 🔬 Neural Model Training & Dataset Tools
- **Custom MLP Neural Classifier**: Fast, deterministic feed-forward network targeting intent routing without runtime LLM overhead.
- **Dataset Management Utilities**: Tools to clean, balance, evaluate, and export training sets (`.pb` Protobuf & `.csv`).

---

## 📋 Table of Contents

- [What is Dense?](#-what-is-dense)
- [Key Capabilities](#-key-capabilities)
- [Quick Start](#-quick-start)
- [Usage Examples](#-usage-examples)
- [Project Architecture](#-project-architecture)
- [Make Targets Reference](#-make-targets-reference)
- [Requirements](#️-requirements)

---

## 🚀 Quick Start

### 1. Build All Binaries
```bash
make build

```

### 2. Run Test Suite

```bash
make test

```

### 3. Train the Local Model

```bash
make train
# Or run manually:
./bin/dense_train -data=data/training/command_examples.pb -model=data/models/dense/model.gob

```

### 4. Launch Interactive Assistant

```bash
make llm

```

### 5. Enable Workspace Auto-Watch Mode

```bash
make watch

```

---

## 💡 Usage Examples

### Interactive Session Slash Commands

Inside the `dense_llm` interactive shell, use built-in slash commands:

* `/new [session]` — Create a new conversation session
* `/list` — List active sessions
* `/switch <session>` — Switch session focus
* `/file <path>` — Direct prompt targets to a specific Go source file

### Natural Language Prompting

```text
> create function CalculateTax(total float64, rate float64) float64
> add method ProcessOrder on OrderService(ctx context.Context) error
> add unit test for CalculateTax
> create struct UserConfig
> add import "net/http"

```

### One-Shot Prompt Command

Execute standard operations without entering interactive mode:

```bash
./bin/dense_llm -model=data/models/dense/model.gob -prompt "create function Sum(a int, b int) int"

```

---

## 🏗 Project Architecture

```text
golangast-dense/
├── cmd/
│   ├── dense/            # Core CLI entry point (dense fix / dense generate)
│   ├── server/           # Built-in HTTP scaffold server
│   └── tools/
│       ├── dense_clean/  # Dataset balancing & preprocessing tool
│       ├── dense_eval/   # Confusion matrix & model evaluation suite
│       ├── dense_llm/    # Interactive TUI & one-shot prompt executor
│       ├── dense_once/   # Single suggestion pass runner
│       ├── dense_study/  # Experimental model research runner
│       ├── dense_train/  # Neural network training runner
│       └── dense_watch/  # Autonomous workspace file watcher & fixer
├── internal/
│   └── ai/
│       └── dense/        # MLP Engine, AST Parser, Indexer & Refactoring core
├── data/
│   ├── models/dense/     # Pre-trained model artifacts (.gob, .json)
│   └── training/         # Protobuf and CSV command datasets
└── Makefile              # Task orchestration script

```

---

## 🛠 Make Targets Reference

| Target | Command | Description |
| --- | --- | --- |
| `build` | `make build` | Compiles all binaries into `./bin` |
| `test` | `make test` | Runs all workspace unit and integration tests |
| `train` | `make train` | Builds and trains the local neural model |
| `llm` | `make llm` | Launches the interactive shell CLI |
| `watch` | `make watch` | Launches `dense_watch` with self-healing enabled |
| `fmt` | `make fmt` | Formats Go codebase using `gofmt` |
| `tidy` | `make tidy` | Runs `go mod tidy` |
| `clean` | `make clean` | Purges compiled `./bin` binaries |

---

## 📊 GitHub Stats & Coding Activity

<div align="center">
<img src="https://github-profile-summary-cards.vercel.app/api/cards/profile-details?username=golangast&theme=radical" alt="GitHub Profile Summary" />

<img src="https://github-readme-tech-stack.vercel.app/api/cards?username=golangast&theme=radical" alt="GitHub Tech Stack" />

<a href="https://github.com/golangast">
<img src="https://github-readme-activity-graph.vercel.app/graph?username=golangast&theme=radical" alt="GitHub Activity Graph" />
</a>

<img src="https://github-readme-stats.vercel.app/api?username=golangast&show_icons=true&theme=radical&hide_border=true" alt="GitHub Stats" />
<img src="https://github-readme-stats.vercel.app/api/top-langs/?username=golangast&layout=compact&theme=radical&hide_border=true" alt="Top Languages" />

<img src="https://streak-stats.demolab.com/?user=golangast&theme=radical&hide_border=true" alt="GitHub Streak" />
</div>

---

## ⚙️ Requirements

* **Go**: 1.26 or higher
* **Git** (optional): Recommended for auto-restore features during `make watch`.

```

```