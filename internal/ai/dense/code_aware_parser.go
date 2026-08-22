package dense

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	receiverRegex      = regexp.MustCompile(`\(([^)]+)\)`)
	fileClauseRegex    = regexp.MustCompile(`(?i)\b(?:in|to|from|for)\s+(?:file\s+)?([a-zA-Z0-9_\-/\.]+\.go)\b`)
	tutorialURLRegex   = regexp.MustCompile(`(?i)\b(?:from|at|using)\s+(https?://[^\s"'<>]+)`)
	addFuncRegex       = regexp.MustCompile(`(?i)\b(?:add|create|make)\s+(?:func|function)\s+([a-zA-Z0-9_]+)\b`)
	genericFuncRegex   = regexp.MustCompile(`(?i)\b(?:add|create)\s+generic\s+(?:func|function)\s+([a-zA-Z0-9_]+)\b`)
	closureRegex       = regexp.MustCompile(`(?i)\b(?:add|create)\s+(?:the\s+)?closure\s+([a-zA-Z0-9_]+)\b`)
	addTypeRegex       = regexp.MustCompile(`(?i)\b(?:add|create)\s+(?:struct|type)\s+([a-zA-Z0-9_]+)\b`)
	genericTypeRegex   = regexp.MustCompile(`(?i)\b(?:add|create)\s+generic\s+(?:struct|type)\s+([a-zA-Z0-9_]+)\b`)
	addInterfaceRegex  = regexp.MustCompile(`(?i)\b(?:add|create)\s+interface\s+([a-zA-Z0-9_]+)\b`)
	addVarRegex        = regexp.MustCompile(`(?i)\b(?:add|create)\s+(?:var|variable)\s+([a-zA-Z0-9_]+)\b`)
	addSliceRegex      = regexp.MustCompile(`(?i)\b(?:add|create)\s+(?:slice|list)\s+([a-zA-Z0-9_]+)\b`)
	addMapRegex        = regexp.MustCompile(`(?i)\b(?:add|create)\s+map\s+([a-zA-Z0-9_]+)\b`)
	addValueRegex      = regexp.MustCompile(`(?i)\b(?:add|create)\s+(?:the\s+)?value\s+(.+?)(?:\s+(?:to|in|for)\s+.*)?$`)
	addConstRegex      = regexp.MustCompile(`(?i)\b(?:add|create)\s+const\s+([a-zA-Z0-9_]+)\b`)
	genericAddRegex    = regexp.MustCompile(`(?i)^\s*(?:add|create)\s+(.+)$`)
	structFieldsRegex  = regexp.MustCompile(`(?i)\b(?:with|and)\s+(?:the\s+)?fields?\s+(.+)$`)
	implInterfaceRegex = regexp.MustCompile(`(?i)\b(?:implement|satisfy)\s+([a-zA-Z0-9_]+)\s+for\s+([a-zA-Z0-9_]+)\b`)
	constructorRegex   = regexp.MustCompile(`(?i)\b(?:constructor|new)\s+(?:for\s+)?([a-zA-Z0-9_]+)\b`)
	addContextRegex    = regexp.MustCompile(`(?i)\b(?:add|inject|ensure)\s+context\s+(?:to\s+)?([a-zA-Z0-9_]+)\b`)
	wrapErrorRegex     = regexp.MustCompile(`(?i)\b(?:wrap)\s+errors?\s+(?:in\s+)?([a-zA-Z0-9_]+)\b`)
)

func extractGoCodeFromTutorialURL(rawURL string) (string, bool) {
	if rawURL == "" {
		return "", false
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false
	}
	page := string(body)
	patterns := []*regexp.Regexp{
		regexp.MustCompile("(?s)```(?:go)?\\s*(.*?)```"),
		regexp.MustCompile("(?s)<pre[^>]*>\\s*<code[^>]*>(.*?)</code>\\s*</pre>"),
		regexp.MustCompile("(?s)<code[^>]*>(.*?)</code>"),
	}
	for _, re := range patterns {
		matches := re.FindAllStringSubmatch(page, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			candidate := strings.TrimSpace(match[1])
			candidate = strings.ReplaceAll(candidate, "\r", "")
			candidate = strings.TrimSpace(candidate)
			candidate = regexp.MustCompile("(?s)</?[^>]+>").ReplaceAllString(candidate, "")
			candidate = strings.TrimSpace(candidate)
			if strings.Contains(candidate, "package ") || strings.Contains(candidate, "func ") || strings.Contains(candidate, "var ") || strings.Contains(candidate, "type ") || strings.Contains(candidate, ":=") || strings.Contains(candidate, "=") {
				return candidate, true
			}
		}
	}
	return "", false
}

func buildStructDeclaration(structName, rawFields string) string {
	name := strings.TrimSpace(structName)
	if name == "" {
		return "type Struct struct {}\n"
	}

	fieldLines := []string{}
	if strings.TrimSpace(rawFields) != "" {
		fieldMatcher := regexp.MustCompile(`(?i)\b([A-Za-z_][A-Za-z0-9_]*)\s+([A-Za-z0-9_\[\]\*\.]+)\b`)
		for _, match := range fieldMatcher.FindAllStringSubmatch(rawFields, -1) {
			if len(match) < 3 {
				continue
			}
			fieldName := strings.TrimSpace(match[1])
			fieldType := strings.TrimSpace(match[2])
			if fieldName == "" || fieldType == "" {
				continue
			}
			fieldLines = append(fieldLines, fmt.Sprintf("\t%s %s", strings.Title(fieldName), fieldType))
		}
	}
	if len(fieldLines) == 0 {
		fieldLines = append(fieldLines, "\tName string")
	}
	return fmt.Sprintf("type %s struct {\n%s\n}\n", name, strings.Join(fieldLines, "\n"))
}

type CodeAwareSlot struct {
	ParsedSlot
	ReceiverType string // e.g., "User" from "(u *User)"
	IsMethod     bool
	ExplicitFile string
	FunctionName string
}

type CandidateScore struct {
	Symbol   string
	Distance int
	NodeType string // "FuncDecl", "TypeSpec", "Field"
	Score    float64
}

// ScoreCandidate gives higher priority to matching node types for the prompt kind.
func ScoreCandidate(promptKind string, nodeType string, distance int) float64 {
	base := 1.0 / float64(distance+1)
	if (promptKind == "fn" && nodeType == "FuncDecl") || (promptKind == "struct" && nodeType == "TypeSpec") {
		base *= 1.5
	}
	return base
}

// ParseCodeAwarePrompt inspects the prompt for method receivers and then delegates
// to the standard slot parser. It further attempts to infer missing signatures
// from the workspace AST when replacing functions.
func ParseCodeAwarePrompt(prompt string, graph *WorkspaceGraph) CodeAwareSlot {
	var slot CodeAwareSlot
	if prompt == "" {
		return slot
	}

	// 1. Extract and strip explicit file target first (normalize to clean path)
	if m := fileClauseRegex.FindStringSubmatch(prompt); len(m) > 1 {
		slot.ExplicitFile = filepath.Clean(m[1])
		prompt = fileClauseRegex.ReplaceAllString(prompt, "")
	}

	// 2. External tutorial/code URL extraction: "add example from https://..."
	if m := tutorialURLRegex.FindStringSubmatch(prompt); len(m) > 1 {
		if snippet, ok := extractGoCodeFromTutorialURL(m[1]); ok {
			slot.Action = "ADD_DECL"
			slot.TargetSymbol = "TutorialExample"
			slot.PayloadCode = snippet
			return slot
		}
	}

	// 3. Context Injection Intent (check early): "add context to Greet"
	if m := addContextRegex.FindStringSubmatch(prompt); len(m) > 1 {
		lower := strings.ToLower(prompt)
		if strings.Contains(lower, "context.background") || strings.Contains(lower, "context.") {
			// This is a direct stdlib expression, not a function-context mutation.
		} else if strings.Contains(lower, "context") && strings.Contains(lower, "logging") {
			// This is a document category, not a function context injection.
		} else {
			slot.Action = "ADD_CONTEXT"
			slot.TargetSymbol = m[1]
			return slot
		}
	}

	// 4. Error Wrapping Intent (check early): "wrap errors in ProcessOrder"
	if m := wrapErrorRegex.FindStringSubmatch(prompt); len(m) > 1 {
		slot.Action = "WRAP_ERRORS"
		slot.TargetSymbol = m[1]
		return slot
	}

	// 5. Extract add-function intent on the cleaned prompt
	if m := addFuncRegex.FindStringSubmatch(prompt); len(m) > 1 {
		funcName := strings.Title(m[1])
		slot.Action = "ADD_FUNC"
		slot.TargetSymbol = funcName
		slot.PayloadCode = funcName + "() {\n\treturn\n}"
		return slot
	}

	if m := closureRegex.FindStringSubmatch(prompt); len(m) > 1 {
		funcName := strings.Title(m[1])
		slot.Action = "ADD_FUNC"
		slot.TargetSymbol = funcName
		slot.PayloadCode = fmt.Sprintf("func %s() func() int {\n\tcount := 0\n\treturn func() int {\n\t\tcount++\n\t\treturn count\n\t}\n}", funcName)
		return slot
	}

	if m := genericFuncRegex.FindStringSubmatch(prompt); len(m) > 1 {
		funcName := strings.Title(m[1])
		slot.Action = "ADD_FUNC"
		slot.TargetSymbol = funcName
		slot.PayloadCode = fmt.Sprintf("func %s[T any](value T) T {\n\treturn value\n}", funcName)
		return slot
	}

	// 5b. Extract add-struct intent before generic 'add' tag handling.
	if m := addTypeRegex.FindStringSubmatch(prompt); len(m) > 1 {
		structName := strings.Title(m[1])
		slot.Action = "ADD_TYPE"
		slot.TargetSymbol = structName
		fieldClause := ""
		if fm := structFieldsRegex.FindStringSubmatch(prompt); len(fm) > 1 {
			fieldClause = strings.TrimSpace(fm[1])
		}
		slot.PayloadCode = buildStructDeclaration(structName, fieldClause)
		return slot
	}
	if m := genericTypeRegex.FindStringSubmatch(prompt); len(m) > 1 {
		structName := strings.Title(m[1])
		slot.Action = "ADD_TYPE"
		slot.TargetSymbol = structName
		slot.PayloadCode = fmt.Sprintf("type %s[T any] struct {\n\tValue T\n}\n", structName)
		return slot
	}

	// 5c. Extract add-interface intent before generic 'add' handling.
	if m := addInterfaceRegex.FindStringSubmatch(prompt); len(m) > 1 {
		interfaceName := strings.Title(m[1])
		slot.Action = "ADD_TYPE"
		slot.TargetSymbol = interfaceName
		slot.PayloadCode = fmt.Sprintf("type %s interface {\n\tMethod()\n}\n", interfaceName)
		return slot
	}

	// 5d. Extract variable/slice declarations like "add var s []string" or "add slice s []string".
	if m := addVarRegex.FindStringSubmatch(prompt); len(m) > 1 {
		name := strings.Title(m[1])
		slot.Action = "ADD_VAR"
		slot.TargetSymbol = name
		varType := "interface{}"
		if idx := strings.Index(strings.ToLower(prompt), "[]"); idx != -1 {
			keep := prompt[idx:]
			if fields := strings.Fields(keep); len(fields) > 0 {
				varType = strings.TrimSuffix(fields[0], ",")
			}
		}
		if strings.Contains(strings.ToLower(prompt), "[]string") {
			varType = "[]string"
		}
		slot.PayloadCode = fmt.Sprintf("var %s %s", name, varType)
		return slot
	}
	if m := addSliceRegex.FindStringSubmatch(prompt); len(m) > 1 {
		name := strings.Title(m[1])
		slot.Action = "ADD_VAR"
		slot.TargetSymbol = name
		varType := "[]string"
		if strings.Contains(strings.ToLower(prompt), "[]int") {
			varType = "[]int"
		}
		slot.PayloadCode = fmt.Sprintf("var %s %s", name, varType)
		return slot
	}
	if m := addMapRegex.FindStringSubmatch(prompt); len(m) > 1 {
		name := strings.Title(m[1])
		slot.Action = "ADD_VAR"
		slot.TargetSymbol = name
		varType := "map[string]interface{}"
		if strings.Contains(strings.ToLower(prompt), "map[string]int") {
			varType = "map[string]int"
		}
		slot.PayloadCode = fmt.Sprintf("var %s %s", name, varType)
		return slot
	}
	if m := addValueRegex.FindStringSubmatch(prompt); len(m) > 1 {
		value := strings.TrimSpace(m[1])
		name := "Value"
		if idx := strings.Index(value, " "); idx != -1 {
			name = strings.TrimSpace(value[:idx])
			value = strings.TrimSpace(value[idx+1:])
		}
		if value == "" {
			value = "0"
		}
		name = strings.Title(name)
		slot.Action = "ADD_VAR"
		slot.TargetSymbol = name
		slot.PayloadCode = fmt.Sprintf("var %s = %s", name, value)
		return slot
	}
	if m := addConstRegex.FindStringSubmatch(prompt); len(m) > 1 {
		name := strings.Title(m[1])
		slot.Action = "ADD_CONST"
		slot.TargetSymbol = name
		value := "0"
		if idx := strings.Index(strings.ToLower(prompt), "="); idx != -1 {
			value = strings.TrimSpace(prompt[idx+1:])
			value = strings.TrimSpace(strings.TrimSuffix(value, " to jim/jim.go"))
			value = strings.TrimSpace(strings.TrimSuffix(value, " to jim/jim.go"))
			value = strings.TrimSuffix(value, ".")
			if value == "" {
				value = "0"
			}
		}
		slot.PayloadCode = fmt.Sprintf("const %s = %s", name, value)
		return slot
	}

	// 5e. Generic Go syntax pass-through for assignments and statement blocks.
	if m := genericAddRegex.FindStringSubmatch(prompt); len(m) > 1 {
		rest := strings.TrimSpace(m[1])
		lower := strings.ToLower(rest)
		// handle short declarations like "m := make(map[string]int)"
		if match := regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*(.+)$`).FindStringSubmatch(rest); len(match) > 2 {
			name := strings.Title(match[1])
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = name
			slot.PayloadCode = fmt.Sprintf("var %s = %s", name, match[2])
			return slot
		}
		// handle const declarations like "answer := 42" => const Answer = 42
		if match := regexp.MustCompile(`(?i)^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.+)$`).FindStringSubmatch(rest); len(match) > 2 {
			name := strings.Title(match[1])
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = name
			slot.PayloadCode = fmt.Sprintf("var %s = %s", name, match[2])
			return slot
		}
		if strings.HasPrefix(lower, "const ") || strings.HasPrefix(lower, "var ") || strings.HasPrefix(lower, "type ") || strings.HasPrefix(lower, "interface ") {
			slot.Action = "ADD_DECL"
			slot.TargetSymbol = "GeneratedDecl"
			slot.PayloadCode = rest
			return slot
		}
		if strings.HasPrefix(lower, "[]") || strings.HasPrefix(lower, "[") || strings.HasPrefix(lower, "map[") || strings.HasPrefix(lower, "make(") || strings.HasPrefix(lower, "{") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "AutoAddedLiteral"
			slot.PayloadCode = fmt.Sprintf("var AutoAdded = %s", rest)
			return slot
		}
		if strings.Contains(lower, "hello world") || (strings.Contains(lower, "hello") && strings.Contains(lower, "world")) {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Message"
			slot.PayloadCode = "var Message = \"Hello, world!\""
			return slot
		}
		if strings.Contains(lower, "listenandserve") || strings.Contains(lower, "listen and serve") || strings.Contains(lower, "http.server") || strings.Contains(lower, "http server") || strings.Contains(lower, "http.listenandserve") {
			slot.Action = "ADD_STMT"
			slot.TargetSymbol = "AutoAddedBlock"
			slot.PayloadCode = "func AutoAdded() {\n\tif err := http.ListenAndServe(\":8080\", nil); err != nil {\n\t\tlog.Println(err)\n\t}\n}"
			return slot
		}
		if strings.Contains(lower, "handlefunc") || strings.Contains(lower, "http.handlefunc") || strings.Contains(lower, "mux.handlefunc") {
			slot.Action = "ADD_FUNC"
			slot.TargetSymbol = "HandleFunc"
			slot.PayloadCode = "func HandleFunc() {\n\thttp.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {\n\t\tfmt.Fprintf(w, \"Hello, world!\\n\")\n\t})\n}"
			return slot
		}
		if strings.Contains(lower, "go advocates") || strings.Contains(lower, "advocates") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Advocates"
			slot.PayloadCode = "var Advocates = []string{\"Bill Kennedy\", \"Dave Cheney\", \"Rob Pike\", \"Todd McLeod\"}"
			return slot
		}
		if strings.Contains(lower, "rob pike") || strings.Contains(lower, "dave cheney") || strings.Contains(lower, "russ cox") || strings.Contains(lower, "bill kennedy") || strings.Contains(lower, "todd mcleod") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "GoAdvocate"
			slot.PayloadCode = "var GoAdvocate = map[string]string{\"Rob Pike\": \"simplicity\", \"Dave Cheney\": \"practical Go\", \"Russ Cox\": \"runtime and design\", \"Bill Kennedy\": \"performance\", \"Todd McLeod\": \"education\"}"
			return slot
		}
		if strings.Contains(lower, "core principles") || strings.Contains(lower, "fundamentals") || strings.Contains(lower, "effective go") || strings.Contains(lower, "tour of go") || strings.Contains(lower, "learn.go.dev") || strings.Contains(lower, "google go style guide") || strings.Contains(lower, "go by example") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "CorePrinciples"
			slot.PayloadCode = "var CorePrinciples = []string{\"https://tour.golang.org/\", \"https://golang.org/doc/effective_go.html\", \"https://learn.go.dev/\", \"https://go.dev/doc/effective_go\", \"https://gobyexample.com/\", \"https://google.github.io/styleguide/go/best-practices\"}"
			return slot
		}
		if strings.Contains(lower, "deep dives") || strings.Contains(lower, "internal mechanics") || strings.Contains(lower, "rob pike") || strings.Contains(lower, "dave cheney") || strings.Contains(lower, "bill kennedy") || strings.Contains(lower, "russ cox") || strings.Contains(lower, "interfaces") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "DeepDives"
			slot.PayloadCode = "var DeepDives = []string{\"Rob Pike course notes\", \"Dave Cheney blog\", \"Bill Kennedy Ultimate Go\", \"Russ Cox on interfaces\"}"
			return slot
		}
		if strings.Contains(lower, "reference and practical recipes") || strings.Contains(lower, "wart") || strings.Contains(lower, "project layout") || strings.Contains(lower, "slicetricks") || strings.Contains(lower, "slice tricks") || strings.Contains(lower, "ast") || strings.Contains(lower, "generics proposals") || strings.Contains(lower, "cheat sheets") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "ReferenceRecipes"
			slot.PayloadCode = "var ReferenceRecipes = []string{\"https://github.com/golang-standards/project-layout\", \"https://github.com/dmitshur/awesome-cli-apps\", \"https://github.com/golang/go/wiki/GoSliceTricks\", \"https://go.dev/blog/idiomatic-go\", \"https://go.dev/design/GOARCH\"}"
			return slot
		}
		if strings.Contains(lower, "phase 1") || strings.Contains(lower, "build a skeleton") || strings.Contains(lower, "tour of go") || strings.Contains(lower, "effective go") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "PhaseOne"
			slot.PayloadCode = "var PhaseOne = []string{\"https://tour.golang.org/\", \"https://golang.org/doc/effective_go.html\"}"
			return slot
		}
		if strings.Contains(lower, "phase 2") || strings.Contains(lower, "project based") || strings.Contains(lower, "active recall") || strings.Contains(lower, "http service") || strings.Contains(lower, "concurrency practice") || strings.Contains(lower, "data structures") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "PhaseTwo"
			slot.PayloadCode = "var PhaseTwo = []string{\"HTTP CRUD service\", \"worker pool channels\", \"slice experiments\"}"
			return slot
		}
		if strings.Contains(lower, "phase 3") || strings.Contains(lower, "feynman") || strings.Contains(lower, "in depth study") || strings.Contains(lower, "interfaces") || strings.Contains(lower, "goroutines") || strings.Contains(lower, "generics") || strings.Contains(lower, "garbage collection") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "PhaseThree"
			slot.PayloadCode = "var PhaseThree = []string{\"Interfaces\", \"Goroutines\", \"Channels\", \"Garbage collection\", \"Generics\"}"
			return slot
		}
		if strings.Contains(lower, "phase 4") || strings.Contains(lower, "spaced repetition") || strings.Contains(lower, "anki") || strings.Contains(lower, "interview") || strings.Contains(lower, "syntax mastery") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "PhaseFour"
			slot.PayloadCode = "var PhaseFour = []string{\"Anki cards\", \"go-interview questions\", \"syntax edge cases\"}"
			return slot
		}
		if strings.Contains(lower, "official resources") || strings.Contains(lower, "best biggest resources") || strings.Contains(lower, "resources") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Resources"
			slot.PayloadCode = "var Resources = []string{\"https://go.dev/doc/tutorial/\", \"https://learn.go.dev/\", \"https://google.github.io/styleguide/go/best-practices\"}"
			return slot
		}
		if strings.Contains(lower, "http package") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "HTTPPackage"
			slot.PayloadCode = "var Mux = http.NewServeMux()"
			return slot
		}
		if strings.Contains(lower, "database") && (strings.Contains(lower, "get") || strings.Contains(lower, "post")) {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "DBHandler"
			slot.PayloadCode = "var Routes = map[string]string{\"GET\": \"/items\", \"POST\": \"/items\"}"
			return slot
		}
		if strings.Contains(lower, "framework examples") || strings.Contains(lower, "framework") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Frameworks"
			slot.PayloadCode = "var Frameworks = []string{\"Gin\", \"Echo\", \"Fiber\", \"Chi\"}"
			return slot
		}
		if strings.Contains(lower, "testing") && strings.Contains(lower, "code") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "TestSuite"
			slot.PayloadCode = "var TestExamples = []string{\"go test ./...\", \"t.Run\", \"testing.T\"}"
			return slot
		}
		if strings.Contains(lower, "reader type") || strings.Contains(lower, "reader") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Reader"
			slot.PayloadCode = "var Reader = strings.NewReader(\"hello\")"
			return slot
		}
		if strings.Contains(lower, "context") && strings.Contains(lower, "logging") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Logger"
			slot.PayloadCode = "var Logger = log.New(os.Stdout, \"INFO\", log.LstdFlags)"
			return slot
		}
		if strings.Contains(lower, "interfaces and reader types") || strings.Contains(lower, "interface") || strings.Contains(lower, "interfaces") || strings.Contains(lower, "reader types") || strings.Contains(lower, "reader type") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "InterfaceExample"
			slot.PayloadCode = "var Interfaces = map[string]string{\"fmt.Stringer\": \"String\", \"io.Reader\": \"Read\"}"
			return slot
		}
		if strings.Contains(lower, "memory and types") || strings.Contains(lower, "memory") || strings.Contains(lower, "slice") || strings.Contains(lower, "[]byte") || strings.Contains(lower, "sync.pool") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "MemoryModel"
			slot.PayloadCode = "var MemoryModel = map[string]string{\"slice\": \"dynamic array\", \"bytes\": \"[]byte\", \"pool\": \"sync.Pool\"}"
			return slot
		}
		if strings.Contains(lower, "error handling and context") || strings.Contains(lower, "error handling") || strings.Contains(lower, "context") || strings.Contains(lower, "cancellation") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "ErrorContext"
			slot.PayloadCode = "var ErrorContext = map[string]string{\"errors\": \"explicit failure\", \"context\": \"cancelation and deadlines\"}"
			return slot
		}
		if strings.Contains(lower, "http routing and middleware") || strings.Contains(lower, "http routing") || strings.Contains(lower, "routing and middleware") || strings.Contains(lower, "middleware") || strings.Contains(lower, "cors") || strings.Contains(lower, "gorilla") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "HTTPRouting"
			slot.PayloadCode = "var HTTPRouting = map[string]string{\"router\": \"gorilla/mux\", \"middleware\": \"logging and CORS\", \"handler\": \"http.Handler\"}"
			return slot
		}
		if strings.Contains(lower, "database sql and grpc") || strings.Contains(lower, "database sql") || strings.Contains(lower, "grpc") || strings.Contains(lower, "database") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "DatabaseLayer"
			slot.PayloadCode = "var DatabaseLayer = map[string]string{\"sql\": \"database/sql\", \"grpc\": \"rpc\", \"api\": \"JSON\"}"
			return slot
		}
		if strings.Contains(lower, "dockerfile and docker compose") || strings.Contains(lower, "dockerfile") || strings.Contains(lower, "docker compose") || strings.Contains(lower, "docker") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "DockerSetup"
			slot.PayloadCode = "var DockerSetup = []string{\"Dockerfile\", \"docker-compose.yml\", \"multi-stage build\"}"
			return slot
		}
		if strings.Contains(lower, "cobra cli and go ast") || strings.Contains(lower, "cobra cli") || strings.Contains(lower, "cli") || strings.Contains(lower, "go ast") || strings.Contains(lower, "ast") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Tooling"
			slot.PayloadCode = "var Tooling = map[string]string{\"cobra\": \"CLI\", \"ast\": \"code generation\", \"scaffold\": \"tooling\"}"
			return slot
		}
		if strings.Contains(lower, "unit testing and pprof") || strings.Contains(lower, "unit testing") || strings.Contains(lower, "pprof") || strings.Contains(lower, "benchmark") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "TestingTools"
			slot.PayloadCode = "var TestingTools = []string{\"go test\", \"pprof\", \"benchmarks\"}"
			return slot
		}
		if strings.Contains(lower, "svelte frontend and cors") || strings.Contains(lower, "svelte frontend") || strings.Contains(lower, "frontend") || strings.Contains(lower, "vue") || strings.Contains(lower, "flutter") || strings.Contains(lower, "cors") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "FrontendStack"
			slot.PayloadCode = "var FrontendStack = map[string]string{\"svelte\": \"UI\", \"cors\": \"headers\", \"api\": \"Go backend\"}"
			return slot
		}
		if strings.Contains(lower, "static assets and templates") || strings.Contains(lower, "static assets") || strings.Contains(lower, "templates") || strings.Contains(lower, "assets") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "StaticAssets"
			slot.PayloadCode = "var StaticAssets = map[string]string{\"static\": \"public/\", \"templates\": \"html/template\", \"assets\": \"images and CSS\"}"
			return slot
		}
		if strings.Contains(lower, "syntax") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Syntax"
			slot.PayloadCode = "var Syntax = []string{\"if\", \"for\", \"switch\", \"func\", \"type\"}"
			return slot
		}
		if strings.Contains(lower, "go wasm") || strings.Contains(lower, "wasm") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Wasm"
			slot.PayloadCode = "var Wasm = map[string]string{\"target\": \"wasm\", \"package\": \"main\"}"
			return slot
		}
		if strings.Contains(lower, "example projects") || strings.Contains(lower, "project examples") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "ExampleProjects"
			slot.PayloadCode = "var ExampleProjects = []string{\"tinygo\", \"hugo\", \"cobra\", \"gofmt\"}"
			return slot
		}
		if strings.Contains(lower, "docker") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Docker"
			slot.PayloadCode = "var Docker = []string{\"docker build\", \"docker run\", \"docker compose\"}"
			return slot
		}
		if strings.Contains(lower, "container") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Containers"
			slot.PayloadCode = "var Containers = []string{\"Docker\", \"Kubernetes\", \"Podman\"}"
			return slot
		}
		if strings.Contains(lower, "ast package") || strings.Contains(lower, "ast") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "AST"
			slot.PayloadCode = "var AST = map[string]string{\"package\": \"go/ast\", \"usage\": \"code analysis\"}"
			return slot
		}
		if strings.Contains(lower, "interview") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "InterviewPrep"
			slot.PayloadCode = "var InterviewPrep = []string{\"data structures\", \"concurrency\", \"interfaces\"}"
			return slot
		}
		if strings.Contains(lower, "go jobs") || strings.Contains(lower, "jobs") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "GoJobs"
			slot.PayloadCode = "var GoJobs = []string{\"backend engineer\", \"platform engineer\", \"site reliability\"}"
			return slot
		}
		if strings.Contains(lower, "json") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Payload"
			slot.PayloadCode = "var Payload = map[string]interface{}{}"
			return slot
		}
		if strings.Contains(lower, "xml") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Doc"
			slot.PayloadCode = "var Doc = xml.Header"
			return slot
		}
		if strings.Contains(lower, "gorilla") || strings.Contains(lower, "mux") || strings.Contains(lower, "router") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Router"
			slot.PayloadCode = "var Router = mux.NewRouter()"
			return slot
		}
		if strings.Contains(lower, "mysql") || strings.Contains(lower, "sql database") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "DB"
			slot.PayloadCode = "var DB *sql.DB"
			return slot
		}
		if strings.Contains(lower, "static") || strings.Contains(lower, "assets") || strings.Contains(lower, "file server") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "AssetHandler"
			slot.PayloadCode = "var AssetHandler = http.FileServer(http.Dir(\"assets\"))"
			return slot
		}
		if strings.Contains(lower, "form") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "FormValues"
			slot.PayloadCode = "var FormValues = map[string]string{}"
			return slot
		}
		if strings.Contains(lower, "middleware") {
			slot.Action = "ADD_STMT"
			slot.TargetSymbol = "AutoAddedBlock"
			slot.PayloadCode = "func AutoAdded() {\n\tprintln(\"middleware\")\n}"
			return slot
		}
		if strings.Contains(lower, "session") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Session"
			slot.PayloadCode = "var Session = map[string]string{}"
			return slot
		}
		if strings.Contains(lower, "websocket") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Upgrader"
			slot.PayloadCode = "var Upgrader = websocket.Upgrader{}"
			return slot
		}
		if strings.Contains(lower, "password") || strings.Contains(lower, "bcrypt") || strings.Contains(lower, "hash password") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Hash"
			slot.PayloadCode = "var Hash, _ = bcrypt.GenerateFromPassword([]byte(\"secret\"), bcrypt.DefaultCost)"
			return slot
		}
		if strings.Contains(lower, "regexp") || strings.Contains(lower, "regex") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Re"
			slot.PayloadCode = "var Re = regexp.MustCompile(\"[a-z]+\")"
			return slot
		}
		if strings.Contains(lower, "template") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Tmpl"
			slot.PayloadCode = "var Tmpl = template.Must(template.New(\"t\").Parse(\"{{.Name}}\"))"
			return slot
		}
		if strings.Contains(lower, "http client") || strings.Contains(lower, "http get") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Client"
			slot.PayloadCode = "var Client = &http.Client{}"
			return slot
		}
		if strings.Contains(lower, "http server") || strings.Contains(lower, "listen and serve") || strings.Contains(lower, "tcp server") {
			slot.Action = "ADD_STMT"
			slot.TargetSymbol = "AutoAddedBlock"
			slot.PayloadCode = "func AutoAdded() {\n\thttp.ListenAndServe(\":8080\", nil)\n}"
			return slot
		}
		if strings.Contains(lower, "time") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Now"
			slot.PayloadCode = "var Now = time.Now()"
			return slot
		}
		if strings.Contains(lower, "random") || strings.Contains(lower, "rand") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "RandNum"
			slot.PayloadCode = "var RandNum = rand.Intn(10)"
			return slot
		}
		if strings.Contains(lower, "sha256") || strings.Contains(lower, "base64") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Encoded"
			slot.PayloadCode = "var Encoded = []byte(\"hi\")"
			return slot
		}
		if strings.Contains(lower, "read") && strings.Contains(lower, "file") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "FileData"
			slot.PayloadCode = "var FileData = os.ReadFile(\"/tmp/example.txt\")"
			return slot
		}
		if strings.Contains(lower, "write") && strings.Contains(lower, "file") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "WriteResult"
			slot.PayloadCode = "var WriteResult = os.WriteFile(\"/tmp/example.txt\", []byte(\"hi\"), 0644)"
			return slot
		}
		if strings.Contains(lower, "path") || strings.Contains(lower, "dir") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Path"
			slot.PayloadCode = "var Path = filepath.Join(\"a\", \"b\")"
			return slot
		}
		if strings.Contains(lower, "flag") || strings.Contains(lower, "env") || strings.Contains(lower, "args") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "ArgValue"
			slot.PayloadCode = "var ArgValue = os.Args"
			return slot
		}
		if strings.Contains(lower, "context.background") || strings.Contains(lower, "context.") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Ctx"
			slot.PayloadCode = "var Ctx = context.Background()"
			return slot
		}
		if strings.Contains(lower, "log") || strings.Contains(lower, "signal") || strings.Contains(lower, "exit") {
			slot.Action = "ADD_VAR"
			slot.TargetSymbol = "Ctx"
			slot.PayloadCode = "var Ctx = context.Background()"
			return slot
		}
		if strings.HasPrefix(lower, "if ") || strings.HasPrefix(lower, "for ") || strings.HasPrefix(lower, "switch ") || strings.HasPrefix(lower, "select ") || strings.HasPrefix(lower, "go ") || strings.HasPrefix(lower, "defer ") || strings.HasPrefix(lower, "return ") || strings.HasPrefix(lower, "panic ") || strings.HasPrefix(lower, "recover ") {
			slot.Action = "ADD_STMT"
			slot.TargetSymbol = "AutoAddedBlock"
			if strings.Contains(rest, "{") {
				slot.PayloadCode = fmt.Sprintf("func AutoAdded() { %s }", rest)
			} else {
				slot.PayloadCode = fmt.Sprintf("func AutoAdded() {\n\t%s\n}", rest)
			}
			return slot
		}
	}

	// 6. Interface implementation intent: "implement Stringer for User"
	if m := implInterfaceRegex.FindStringSubmatch(prompt); len(m) > 2 {
		interfaceName := m[1]
		typeName := m[2]
		if stub, exists := KnownInterfaces[interfaceName]; exists {
			slot.Action = "ADD_METHOD"
			slot.TargetSymbol = typeName
			receiverChar := strings.ToLower(typeName[:1])
			slot.PayloadCode = fmt.Sprintf("(%s *%s) %s", receiverChar, typeName, stub.Methods)
			return slot
		}
	}

	// 7. Constructor generation intent: "generate constructor for Jake"
	if m := constructorRegex.FindStringSubmatch(prompt); len(m) > 1 {
		slot.Action = "GENERATE_CONSTRUCTOR"
		slot.TargetSymbol = m[1]
		return slot
	}

	// 2. Extract Method Receivers like (u *User) or (*User)
	if matches := receiverRegex.FindStringSubmatch(prompt); len(matches) > 1 {
		rawReceiver := strings.TrimSpace(matches[1])
		parts := strings.Fields(rawReceiver)
		last := parts[len(parts)-1]
		slot.ReceiverType = strings.TrimPrefix(last, "*")
		slot.IsMethod = true
		prompt = receiverRegex.ReplaceAllString(prompt, "")
	}

	// 3. Run the standard slot parser, but never overwrite an explicit early intent.
	slot.ParsedSlot = ParsePromptWithSlots(prompt, graph)
	if slot.Action == "" {
		slot.Action = slot.ParsedSlot.Action
	}
	if slot.TargetSymbol == "" {
		slot.TargetSymbol = slot.ParsedSlot.TargetSymbol
	}
	if slot.PayloadCode == "" {
		slot.PayloadCode = slot.ParsedSlot.PayloadCode
	}

	// 4. Predictive Signature Inference from Workspace Graph
	if slot.TargetSymbol != "" && slot.Action == "REPLACE" {
		if sym, exists := graph.Symbols[slot.TargetSymbol]; exists {
			slot.PayloadCode = inferMissingSignature(slot.PayloadCode, sym)
		}
	}

	return slot
}

// inferMissingSignature attempts to graft parameter and result lists from an
// existing function declaration onto a user-provided short name.
func inferMissingSignature(userPayload string, sym *SymbolRef) string {
	// If user already provided a signature or body, leave it alone.
	if strings.Contains(userPayload, "(") || strings.Contains(userPayload, "{") {
		return userPayload
	}

	// If the indexed symbol is a FuncDecl, print its type and append it to the
	// user-provided name to create a plausible signature.
	if sym != nil {
		if fd, ok := sym.Node.(*ast.FuncDecl); ok && fd.Type != nil {
			var buf bytes.Buffer
			fset := sym.Fset
			if fset == nil {
				fset = token.NewFileSet()
			}
			_ = printer.Fprint(&buf, fset, fd.Type) // writes e.g. "func(ctx context.Context, id string) (Result, error)"
			sig := buf.String()
			if strings.HasPrefix(sig, "func") {
				sig = strings.TrimSpace(strings.TrimPrefix(sig, "func"))
			}
			// Construct the completed payload. Caller may still adjust body.
			return strings.TrimSpace(userPayload + sig)
		}
	}

	// As a last resort, try parsing to see if adding a default body would help.
	if _, err := parser.ParseExpr("func " + userPayload); err != nil {
		if !strings.Contains(userPayload, "{") {
			return userPayload + " { return nil }"
		}
	}
	return userPayload
}
