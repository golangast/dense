<div align="center">

[![Go Version](https://img.shields.io/github/go-mod/go-version/golangast/dense?style=for-the-badge&logo=go&logoColor=white)](https://github.com/golangast/dense)
[![GoDoc](https://img.shields.io/badge/godoc-reference-007d9c.svg?style=for-the-badge&logo=go&logoColor=white)](https://pkg.go.dev/github.com/golangast/dense)
[![Go Report Card](https://goreportcard.com/badge/github.com/golangast/dense?style=for-the-badge)](https://goreportcard.com/report/github.com/golangast/dense)
[![Build Status](https://img.shields.io/badge/build-passing-brightgreen.svg?style=for-the-badge&logo=github-actions)](https://github.com/golangast/dense)
[![Coverage](https://img.shields.io/badge/coverage-85%25-brightgreen.svg?style=for-the-badge&logo=codecov)](https://github.com/golangast/dense)
[![Status](https://img.shields.io/badge/Status-Beta-orange.svg?style=for-the-badge)](https://github.com/golangast/dense)

[![GitHub License](https://img.shields.io/github/license/golangast/dense?style=for-the-badge)](https://github.com/golangast/dense/blob/main/LICENSE)
[![GitHub Stars](https://img.shields.io/github/stars/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/stargazers)
[![GitHub Forks](https://img.shields.io/github/forks/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/network/members)
[![GitHub Issues](https://img.shields.io/github/issues/golangast/dense?style=for-the-badge&logo=github)](https://github.com/golangast/dense/issues)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=for-the-badge)](http://makeapullrequest.com)

</div>

<br />

> **AST-Aware Natural Language Code Intelligence and Interactive Refactoring Engine for Go.**

---

## ⚡ What is Dense?

[cite_start]**Dense** is an AST-aware neural code engine written in Go[cite: 342, 343]. [cite_start]Instead of relying on raw string concatenation or heavy external LLM APIs, Dense combines a lightweight feed-forward neural network (MLP), deterministic intent classification, and Go AST indexers to transform natural language developer prompts into precise, syntactically valid Go code modifications[cite: 343, 351, 355].

[cite_start]It acts as both an interactive assistant and an active workspace daemon, executing type-checked code generation, automated refactoring, and self-healing diagnostic fixes[cite: 343, 350, 354, 355, 356].

---

## 🔥 Key Capabilities

### 🧠 Natural Language to AST Transformation
- [cite_start]**Intent Parsing**: Classifies prompts into actions (`ADD_FUNC`, `ADD_TYPE`, `REPLACE`, `INJECT_TAGS`, `ADD_IMPORT`, etc.)[cite: 351].
- [cite_start]**Signature Inference**: Automatically infers missing parameter and return types by referencing workspace symbols[cite: 352].
- [cite_start]**Receiver-Aware Methods**: Generates methods bound directly to target receiver structs[cite: 347, 371].
- [cite_start]**AST Safety Guarantee**: All code additions are inserted directly into the Go Abstract Syntax Tree (AST) to ensure structural integrity[cite: 343, 347].

### 🛠 Active Workspace Monitoring & Healing (`make watch`)
- [cite_start]**Real-time File Supervision**: Watches the project using `fsnotify` and evaluates changes instantly[cite: 356, 358].
- [cite_start]**Automated Compiler Diagnostics & Fixes**: Intercepts `go` compiler errors (missing braces, broken imports, missing commas) and iteratively applies conservative fixes[cite: 354].
- [cite_start]**Git Rollback Guard**: Supports optional auto-restoration from `git HEAD` if auto-applied code changes fail verification[cite: 357].

### 💬 Interactive Shell & Multi-Session CLI
- [cite_start]**Interactive Multi-Session Assistant**: Manage concurrent conversation threads, attach individual target files, and execute refactorings on the fly[cite: 345, 346].
- [cite_start]**One-Shot Execution**: Run single prompt commands directly from standard CLI arguments.
- [cite_start]**Workspace Navigation**: Manage project structure (creating/deleting directories and files) seamlessly through prompt interactions[cite: 348].

### 🔬 Neural Model Training & Dataset Tools
- [cite_start]**Custom MLP Neural Classifier**: Fast, deterministic feed-forward network targeting intent routing without runtime LLM overhead[cite: 342, 343].
- [cite_start]**Dataset Management Utilities**: Tools to clean, balance, evaluate, and export training sets (`.pb` Protobuf & `.csv`)[cite: 344, 367, 368].

---

## 📋 Table of Contents

- [What It Can Do](#-what-it-can-do)
- [Quick Start](#-quick-start)
- [Usage Examples](#-usage-examples)
- [Project Architecture](#-project-architecture)
- [Make Targets](#-make-targets)
- [Requirements](#-requirements)

---

## 🚀 Quick Start

### 1. Build All Binaries
```bash
make build