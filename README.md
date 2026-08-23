# Dense

Dense is a compact Go project for training and using a dense neural model that classifies developer prompts and turns them into safe code actions. The system combines a small MLP model, deterministic intent parsing, and Go AST-aware validation to support interactive Go editing.

## What it can do

### 1. Train the dense model

The project includes a training tool that loads command examples, learns a simple dense MLP classifier, and saves the model to disk.

```bash
go run ./cmd/tools/dense_train -data=data/training/command_examples.pb -model=data/models/dense/model.gob
```

This is useful for:
- training a classifier over command examples
- updating the model with new prompt patterns
- generating a reusable model for interactive editing

### 2. Run the interactive Go assistant

The CLI supports an interactive shell for prompt-driven code work.

```bash
go run ./cmd/tools/dense_llm -model=data/models/dense/model.gob
```

In interactive mode you can:
- type natural-language Go editing requests
- ask for code generation or file operations
- keep multiple independent conversations open
- target a different Go file per conversation
- use slash commands to manage sessions and files

### 3. Support multiple conversations

The assistant maintains multiple named conversations at once.

Slash commands:
- /new [name]
- /list
- /switch <name>
- /delete <name>
- /current
- /file <path>
- /help

This makes it easy to work on several code threads or separate tasks without mixing context.

### 4. Target files for direct editing

Each conversation can point at a Go file to update.

Examples:

```text
/file ./internal/ai/dense/example.go
```

You can also set a default file at startup:

```bash
go run ./cmd/tools/dense_llm -model=data/models/dense/model.gob -file ./example.go
```

### 5. Generate Go code from intent

The agent can infer intents such as:
- create function
- create method
- add import
- add struct or type
- add unit test
- insert error checks
- create helper return patterns
- edit or delete existing symbols

The formatter deterministically renders Go snippets using the Go AST and writes valid source code when possible.

### 6. Modify Go files safely with AST-based edits

Rather than blindly appending text, the CLI uses AST manipulation for common operations:
- import insertion
- function insertion/replacement
- type and struct insertion
- receiver-aware method generation
- symbol replacement when a function already exists

This reduces malformed output and keeps edits consistent with the existing Go file structure.

### 7. Validate generated code before saving

Before a change is written, the project checks the resulting code with Go parsing and type-check validation using the Go type checker.

Safety features include:
- parse validation
- type validation via go/types
- package-scope symbol inspection
- import safety checks
- rejection of broken AST output before writing to disk

This is especially important when generating new functions that reference custom types or packages that are not yet declared in the target file.

### 8. Understand package-level context

The system inspects the package directory and nearby files to gather context about:
- existing functions
- existing types
- imported packages

This helps the assistant choose better actions when editing a Go package and avoid generating duplicate or conflicting declarations.

### 9. Handle non-Go file operations too

The CLI is not limited to Go code generation. It also supports higher-level file and folder operations, including:
- create file
- edit file
- delete file
- create folder
- delete folder

These actions are routed through the same conversational flow and can be used alongside the Go-editing behavior.

### 10. Run one-shot prompt classification

You can classify and respond to a single prompt without entering the interactive shell:

```bash
go run ./cmd/tools/dense_llm -model=data/models/dense/model.gob -prompt 'create function Sum(a int, b int) int'
```

This is useful for tests, automation, scripting, and quick prompt-debugging.

## Typical usage patterns

### Train a model

```bash
go run ./cmd/tools/dense_train -data=data/training/command_examples.pb -model=data/models/dense/model.gob
```

### Start the interactive assistant

```bash
go run ./cmd/tools/dense_llm -model=data/models/dense/model.gob
```

Then try prompts like:

```text
create function ComputeSum(a int, b int) int
add import "fmt"
create method DoThing on Service(a int) error
add unit test for DoWork
create struct User
create file main.go
```

## Project layout

- cmd/tools/dense_train: training entry point
- cmd/tools/dense_llm: interactive LLM-like assistant and editor
- internal/ai/dense: dense model, feature extraction, classification logic
- internal/ai/training: command example schema and training data support
- data/models/dense: saved trained model artifacts
- data/training: command example corpus

## Notes

This project is intentionally lightweight and deterministic. It favors:
- a small neural model for classification
- explicit parsing for common prompt patterns
- Go AST generation and validation for safe edits

That combination is well suited for simple developer-side automation and structured code editing tasks.

## Learned capabilities (from advocate files & resource links)

Since ingesting advocate notes and linked resources, Dense has acquired these practical, workspace-aware abilities:

- **Workspace indexing**: builds a cross-file `WorkspaceGraph` of symbols and ASTs for precise target resolution.
- **Intent parsing & routing**: maps natural-language prompts to explicit actions (ADD_FUNC, ADD_TYPE, REPLACE, INJECT_TAGS, ADD_VAR, ADD_DECL, etc.).
- **AST-safe edits**: makes top-level and function-level edits via AST helpers (`Append*`, `ReplaceFunctionDecl`, `AppendGenericDecl`) rather than raw text.
- **Signature inference**: when users provide short function names, Dense infers parameter and result lists from existing symbols to create plausible signatures.
- **Webserver scaffolding**: recognizes requests for a web/http server and inserts a safe, uniquely-named server scaffold (StartServer-style) into the requested file.
- **Code extraction from tutorials**: can fetch tutorial pages and extract Go code blocks (```go``` / `<pre><code>`) to insert as declarations.
- **Automated diagnostics & fixes**: runs `go` commands to collect compiler errors, parses the output, and applies conservative fixes (e.g., missing imports) in an iterative `dense fix` loop.
- **REPL & CLI integration**: interactive `dense_llm` REPL and `dense` CLI commands persist AST edits and support one-shot prompts for automation.
- **DOCX / taxonomy extraction**: extracts resource links and taxonomy entries from DOCX documents to inform generation and provide references.
- **Safety-first workflow**: validates generated code with the Go parser/type-checker, formats output, and only writes changes that pass parsing and basic checks.

These capabilities make Dense practical for small-scale code generation, conservative automated fixes, and interactive developer workflows where safety and workspace-awareness are important.
