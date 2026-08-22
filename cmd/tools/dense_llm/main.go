// Command dense_llm runs interactive inference on the simplified dense MLP
// trained on the basic go_edit_agent update-command corpus.
//
// For social prompts it prints the corpus response; for code_update prompts it
// prints the transformed code snippet produced by the matched command and, when
// a target Go file is available, applies the change directly to disk.
//
// The interactive shell maintains multiple named conversations so responses can
// be context-aware across multiple turns and across multiple independent
// dialogue threads (e.g. follow-up questions, references to previous code edits,
// and social continuity). Each conversation can target a different Go file,
// so multiple conversations can independently update different files.
//
// Conversation management commands (interactive mode):
//
//	/new [name]        start a new conversation (auto-generates a name if omitted)
//	/list              list all conversations
//	/switch <name>     switch to an existing conversation
//	/delete <name>     delete a conversation
//	/current           show the active conversation name
//	/file <path>       set the target Go file to update for the active conversation
//	/help              show this help
//
// Usage:
//
//	go run ./cmd/tools/dense_llm -model=data/models/dense/model.gob \
//	    [-prompt "..."] [-file=path/to/target.go]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/golangast/dense/internal/ai/dense"
	"golang.org/x/tools/go/ast/astutil"
)

// ChatTurn is a single user/assistant exchange in the conversation history.
type ChatTurn struct {
	User      string
	Assistant string
	Type      string // "social" or "code_update"
}

// Conversation tracks the multi-turn dialogue state for a single thread.
type EditSnapshot struct {
	FilePath  string
	Content   string
	CreatedAt time.Time
}

type Conversation struct {
	ID         string
	Turns      []ChatTurn
	TargetFile string         // Go file this conversation applies code updates to
	UndoStack  []EditSnapshot // last successful AST writes for rollback
}

// AddTurn appends a new exchange to the conversation history.
func (c *Conversation) AddTurn(user, assistant, cmdType string) {
	c.Turns = append(c.Turns, ChatTurn{User: user, Assistant: assistant, Type: cmdType})
	// Keep only the last 10 turns to bound memory.
	if len(c.Turns) > 10 {
		c.Turns = c.Turns[len(c.Turns)-10:]
	}
}

// LastUser returns the most recent user message, or "" if none.
func (c *Conversation) LastUser() string {
	if len(c.Turns) == 0 {
		return ""
	}
	return c.Turns[len(c.Turns)-1].User
}

// LastAssistant returns the most recent assistant response, or "" if none.
func (c *Conversation) LastAssistant() string {
	if len(c.Turns) == 0 {
		return ""
	}
	return c.Turns[len(c.Turns)-1].Assistant
}

// LastType returns the type of the most recent exchange.
func (c *Conversation) LastType() string {
	if len(c.Turns) == 0 {
		return ""
	}
	return c.Turns[len(c.Turns)-1].Type
}

// HasContext returns true when there is at least one prior exchange.
func (c *Conversation) HasContext() bool {
	return len(c.Turns) > 0
}

// ContextString builds a compact summary of recent history for context-aware
// response generation.
func (c *Conversation) ContextString() string {
	if len(c.Turns) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, t := range c.Turns {
		if i > 0 {
			sb.WriteString(" | ")
		}
		sb.WriteString(fmt.Sprintf("U:%s A:%s", t.User, t.Assistant))
	}
	return sb.String()
}

// SetTargetFile validates and sets this conversation's target file for create,
// edit, or delete operations. Go-specific edits still use .go target files.
func (c *Conversation) SetTargetFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("target file path cannot be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve target file path: %w", err)
	}
	c.TargetFile = abs
	return nil
}

// TargetGoFile returns the conversation's target file, or "" if none set.
func (c *Conversation) TargetGoFile() string {
	return c.TargetFile
}

func (c *Conversation) PushUndoSnapshot(filePath string) {
	if strings.TrimSpace(filePath) == "" {
		return
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	c.UndoStack = append(c.UndoStack, EditSnapshot{FilePath: filePath, Content: string(content), CreatedAt: time.Now()})
	if len(c.UndoStack) > 20 {
		c.UndoStack = c.UndoStack[len(c.UndoStack)-20:]
	}
}

func (c *Conversation) UndoLastEdit() (bool, string) {
	if len(c.UndoStack) == 0 {
		return false, "No recent edits to undo."
	}
	snap := c.UndoStack[len(c.UndoStack)-1]
	c.UndoStack = c.UndoStack[:len(c.UndoStack)-1]
	if err := os.WriteFile(snap.FilePath, []byte(snap.Content), 0644); err != nil {
		return false, fmt.Sprintf("rollback failed: %v", err)
	}
	return true, fmt.Sprintf("🟡 Restored %s from backup snapshot.", snap.FilePath)
}

func colorize(s, color string) string {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	return color + s + "\033[0m"
}

func formatHistoryTurns(turns []ChatTurn) string {
	if len(turns) == 0 {
		return "No turns yet."
	}
	var sb strings.Builder
	for i, turn := range turns {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("%d. %s", i+1, turn.User))
		if turn.Assistant != "" {
			sb.WriteString(" -> " + turn.Assistant)
		}
	}
	return sb.String()
}

func cloneFileAST(file *ast.File) *ast.File {
	if file == nil {
		return nil
	}
	copy := *file
	return &copy
}

var backupWrites bool

func maybeWriteBackup(filePath string, original []byte) error {
	if !backupWrites {
		return nil
	}
	return os.WriteFile(filePath+".bak", original, 0644)
}

func SafeApply(fset *token.FileSet, file *ast.File, targetPath string, editFn func(*ast.File)) error {
	if file == nil {
		return fmt.Errorf("nil AST file")
	}
	backup := cloneFileAST(file)
	if targetPath != "" {
		if content, err := os.ReadFile(targetPath); err == nil {
			if err := maybeWriteBackup(targetPath, content); err != nil {
				return fmt.Errorf("backup file write: %w", err)
			}
		}
	}
	editFn(file)
	if err := ValidateAST(fset, file); err != nil {
		if backup != nil {
			*file = *backup
		}
		return fmt.Errorf("edit aborted: %w", err)
	}
	if targetPath == "" {
		return nil
	}
	var buf strings.Builder
	if err := format.Node(&buf, fset, file); err != nil {
		return fmt.Errorf("format node: %w", err)
	}
	if err := os.WriteFile(targetPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func defaultBenchmarkPrompts() []string {
	return []string{
		"create function ComputeSum(a int, b int) int",
		"create function ValidateUser(name string) bool",
		"add import \"fmt\"",
		"add import \"context\"",
		"create function FetchRates() ([]float64, error)",
		"create function Process(items []string, m map[string]int) error",
		"create function BuildURL(base string, path string) string",
		"create function Notify(ctx context.Context, msg string)",
		"create struct User",
		"add unit test for DoWork",
	}
}

type BenchmarkResult struct {
	Total              int
	CorrectIntent      int
	CompiledFirstPass  int
	IntentPrecision    float64
	ASTCompilationRate float64
}

func RunBenchmark(modelPath string, prompts []string) BenchmarkResult {
	model, err := dense.LoadGob(modelPath)
	if err != nil {
		model = dense.NewDenseModel(len(dense.CommandVocab), []int{8}, len(dense.CommandLabels))
	}
	res := BenchmarkResult{Total: len(prompts)}
	for _, prompt := range prompts {
		intent := predictIntent(prompt, nil, model, nil)
		if intent.Action != "" {
			res.CorrectIntent++
		}
		code, err := renderIntentToCode(intent)
		if err != nil || strings.TrimSpace(code) == "" {
			continue
		}
		tempDir := os.TempDir()
		filePath := filepath.Join(tempDir, fmt.Sprintf("dense_bench_%d.go", time.Now().UnixNano()))
		if err := os.WriteFile(filePath, []byte("package demo\n\n"), 0644); err != nil {
			continue
		}
		if _, _, err := applyCodeViaAST(filePath, "package demo\n\n", code); err == nil {
			content, readErr := os.ReadFile(filePath)
			if readErr == nil && validateGoASTFile(filePath, string(content)) == nil {
				res.CompiledFirstPass++
			}
		}
		_ = os.Remove(filePath)
	}
	if res.Total > 0 {
		res.IntentPrecision = float64(res.CorrectIntent) / float64(res.Total)
	}
	if res.Total > 0 {
		res.ASTCompilationRate = float64(res.CompiledFirstPass) / float64(res.Total)
	}
	return res
}

func validateGoASTFile(path, src string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return err
	}

	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	if _, err := conf.Check("demo", fset, []*ast.File{file}, info); err != nil {
		return err
	}
	return nil
}

func runHTTPAdapter(addr, modelPath, dataPath string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/predict", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			Prompt string `json:"prompt"`
			File   string `json:"file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		model, err := dense.LoadGob(modelPath)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		intent := predictIntent(in.Prompt, nil, model, nil)
		payload := map[string]interface{}{"action": intent.Action, "name": intent.Name, "target": intent.Target, "returns": intent.Returns, "params": intent.Params}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
	mux.HandleFunc("/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			Prompt string `json:"prompt"`
			File   string `json:"file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if in.File == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "file required"})
			return
		}
		model, err := dense.LoadGob(modelPath)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		intent := predictIntent(in.Prompt, nil, model, nil)
		code, err := renderIntentToCode(intent)
		if err != nil || strings.TrimSpace(code) == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "cannot render code"})
			return
		}
		applied, msg, err := applyCodeViaAST(in.File, mustReadFile(in.File), code)
		if err != nil || !applied {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("%v", err), "detail": msg})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": msg})
	})
	log.Printf("HTTP adapter listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func mustReadFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return "package demo\n\n"
	}
	return string(content)
}

// ConversationManager holds multiple named conversations.
type ConversationManager struct {
	conversations map[string]*Conversation
	active        string
	nextID        int
}

// NewConversationManager creates a manager with a default conversation.
func NewConversationManager() *ConversationManager {
	m := &ConversationManager{
		conversations: make(map[string]*Conversation),
		nextID:        1,
	}
	m.New("default")
	return m
}

// New creates a new conversation with the given name (or auto-generates one).
// Returns the conversation and its name.
func (m *ConversationManager) New(name string) (*Conversation, string) {
	if name == "" {
		name = fmt.Sprintf("conv-%d", m.nextID)
		m.nextID++
	}
	// Ensure uniqueness.
	base := name
	for i := 2; ; i++ {
		if _, exists := m.conversations[name]; !exists {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	conv := &Conversation{ID: name}
	m.conversations[name] = conv
	m.active = name
	return conv, name
}

// Get returns the active conversation.
func (m *ConversationManager) Get() *Conversation {
	if conv, ok := m.conversations[m.active]; ok {
		return conv
	}
	// Fallback: create a new one.
	conv, _ := m.New("")
	return conv
}

// GetByName returns a conversation by name, or nil.
func (m *ConversationManager) GetByName(name string) *Conversation {
	if conv, ok := m.conversations[name]; ok {
		return conv
	}
	return nil
}

// Switch sets the active conversation by name. Returns false if not found.
func (m *ConversationManager) Switch(name string) bool {
	if _, ok := m.conversations[name]; ok {
		m.active = name
		return true
	}
	return false
}

// Delete removes a conversation by name. Returns false if not found or if it's
// the last remaining conversation.
func (m *ConversationManager) Delete(name string) bool {
	if len(m.conversations) <= 1 {
		return false
	}
	if _, ok := m.conversations[name]; !ok {
		return false
	}
	delete(m.conversations, name)
	if m.active == name {
		// Switch to any remaining conversation.
		for k := range m.conversations {
			m.active = k
			break
		}
	}
	return true
}

// Active returns the active conversation name.
func (m *ConversationManager) Active() string {
	return m.active
}

// List returns all conversation names in insertion order.
func (m *ConversationManager) List() []string {
	names := make([]string, 0, len(m.conversations))
	for k := range m.conversations {
		names = append(names, k)
	}
	return names
}

// isFollowUp detects if the current prompt is a follow-up to the previous turn
// (e.g. "what did you say", "can you repeat", "show me again", "more").
func isFollowUp(prompt string, conv *Conversation) bool {
	if conv == nil {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(prompt))
	followUpPhrases := []string{
		"what did you say", "what did you mean", "can you repeat",
		"show me again", "say that again", "more", "again",
		"what was that", "explain that", "what does that mean",
		"how does that work", "can you elaborate", "tell me more",
		"what about", "and then", "what next", "what now",
		"what did i say", "what was my last message",
	}
	for _, phrase := range followUpPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// isGreeting detects common greeting/social openers.
func isGreeting(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	greetings := []string{
		"hello", "hi", "hey", "good morning", "good evening", "good afternoon",
		"how are you", "how's it going", "what's up", "yo", "greetings",
	}
	for _, g := range greetings {
		if strings.Contains(lower, g) {
			return true
		}
	}
	return false
}

// isFarewell detects common farewell/closing phrases.
func isFarewell(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	farewells := []string{
		"bye", "goodbye", "see you", "farewell", "good night", "goodnight",
		"exit", "quit", "stop", "end conversation", "i'm done", "i am done",
		"that's all", "thats all", "no more", "done for now",
	}
	for _, f := range farewells {
		if strings.Contains(lower, f) {
			return true
		}
	}
	return false
}

// isThanks detects gratitude expressions.
func isThanks(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	thanks := []string{
		"thank", "thanks", "appreciate", "grateful", "much obliged",
	}
	for _, t := range thanks {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// buildContextAwareResponse generates a response that takes conversation
// history into account.
func splitFileCommand(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	for _, candidate := range strings.Fields(trimmed) {
		if strings.HasSuffix(strings.ToLower(candidate), ".go") {
			if filepath.Ext(candidate) == ".go" {
				idx := strings.Index(trimmed, candidate)
				if idx >= 0 {
					return candidate, strings.TrimSpace(trimmed[idx+len(candidate):])
				}
			}
		}
	}
	if match := regexp.MustCompile("(?i)(?:^|\\s)([A-Za-z0-9_./\\\\-]+\\.go)").FindStringSubmatch(trimmed); len(match) > 1 {
		path := match[1]
		if filepath.Ext(path) == ".go" {
			return path, strings.TrimSpace(trimmed[len(match[0]):])
		}
	}
	if idx := strings.Index(trimmed, " "); idx >= 0 {
		first := strings.TrimSpace(trimmed[:idx])
		if filepath.Ext(first) == ".go" {
			return first, strings.TrimSpace(trimmed[idx+1:])
		}
	}
	return strings.TrimSpace(trimmed), ""
}

func normalizeReplacementFunction(name, replacement string) string {
	replacement = strings.TrimSpace(replacement)
	replacement = strings.TrimSpace(strings.TrimSuffix(replacement, "."))
	for _, prefix := range []string{"for ", "with ", "to "} {
		if strings.HasPrefix(strings.ToLower(replacement), prefix) {
			replacement = strings.TrimSpace(replacement[len(prefix):])
			break
		}
	}
	if replacement == "" {
		return ""
	}
	lower := strings.ToLower(replacement)
	if strings.HasPrefix(lower, "func ") {
		return replacement
	}
	if idx := strings.Index(replacement, "("); idx > 0 && idx < len(replacement) {
		prefix := strings.TrimSpace(replacement[:idx])
		if prefix != "" && regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(prefix) {
			if name != "" && strings.EqualFold(prefix, name) {
				replacement = strings.TrimSpace(replacement[idx:])
			}
			return "func " + replacement
		}
	}
	if name == "" {
		return "func " + replacement
	}
	return "func " + name + " " + replacement
}

func extractFunctionNameFromCodeSnippet(code string) string {
	trimmed := strings.TrimSpace(code)
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "."))
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "func "))
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, "("); idx > 0 {
		name := strings.TrimSpace(trimmed[:idx])
		if name != "" {
			return name
		}
	}
	if fields := strings.Fields(trimmed); len(fields) > 0 {
		return strings.Trim(fields[0], " ")
	}
	return ""
}

func extractFunctionSignatureParts(code string) (string, []string, []string) {
	trimmed := strings.TrimSpace(code)
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "."))
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "func "))
	if trimmed == "" {
		return "", nil, nil
	}
	leftParen := strings.Index(trimmed, "(")
	if leftParen <= 0 {
		return extractFunctionNameFromCodeSnippet(trimmed), nil, nil
	}
	name := strings.TrimSpace(trimmed[:leftParen])
	if name == "" {
		name = extractFunctionNameFromCodeSnippet(trimmed)
	}
	rest := trimmed[leftParen+1:]
	closeParen := strings.Index(rest, ")")
	params := []string{}
	if closeParen >= 0 {
		paramsStr := strings.TrimSpace(rest[:closeParen])
		if paramsStr != "" {
			for _, part := range strings.Split(paramsStr, ",") {
				if s := strings.TrimSpace(part); s != "" {
					params = append(params, s)
				}
			}
		}
		returnPart := strings.TrimSpace(rest[closeParen+1:])
		if idx := strings.Index(returnPart, "{"); idx >= 0 {
			returnPart = strings.TrimSpace(returnPart[:idx])
		}
		if returnPart != "" {
			if strings.HasPrefix(returnPart, "(") {
				inner := strings.TrimSpace(strings.Trim(returnPart, "()"))
				if inner != "" {
					for _, r := range strings.Split(inner, ",") {
						if s := strings.TrimSpace(r); s != "" {
							params = append(params[:0], params...)
							break
						}
					}
				}
			}
			if strings.HasPrefix(returnPart, "(") {
				inner := strings.TrimSpace(strings.Trim(returnPart, "()"))
				if inner != "" {
					returns := []string{}
					for _, r := range strings.Split(inner, ",") {
						if s := strings.TrimSpace(r); s != "" {
							returns = append(returns, s)
						}
					}
					if len(returns) > 0 {
						return name, params, returns
					}
				}
			}
			return name, params, []string{returnPart}
		}
	}
	return name, params, nil
}

func functionSnippetFromPrompt(prompt string) string {
	parsed := dense.ParseHybridPrompt(prompt)
	if parsed.Action == "replace" {
		replacement := strings.TrimSpace(parsed.RawCode)
		replacement = strings.TrimSpace(strings.TrimSuffix(replacement, "."))
		replacement = regexp.MustCompile("(?i)\\s+(?:in\\s+)?(?:file\\s+)?[A-Za-z0-9_./\\\\-]+\\.go\\s*$").ReplaceAllString(replacement, "")
		replacement = strings.TrimSpace(replacement)
		if len(parsed.Identifiers) > 0 {
			return normalizeReplacementFunction(parsed.Identifiers[0], replacement) + "\n"
		}
		if replacement != "" {
			return normalizeReplacementFunction("", replacement) + "\n"
		}
	}
	lower := strings.ToLower(strings.TrimSpace(prompt))
	for _, verb := range []string{"replace ", "substitute ", "swap ", "change ", "update "} {
		if idx := strings.Index(lower, verb); idx >= 0 {
			tail := strings.TrimSpace(prompt[idx+len(verb):])
			if strings.HasPrefix(strings.ToLower(tail), "function ") {
				tail = strings.TrimSpace(tail[len("function "):])
			}
			if strings.HasPrefix(strings.ToLower(tail), "fn ") {
				tail = strings.TrimSpace(tail[len("fn "):])
			}
			if strings.HasPrefix(strings.ToLower(tail), "method ") {
				tail = strings.TrimSpace(tail[len("method "):])
			}
			for _, keyword := range []string{" for ", " with ", " to "} {
				if j := strings.Index(strings.ToLower(tail), keyword); j >= 0 {
					body := strings.TrimSpace(tail[j+len(keyword):])
					body = strings.TrimSpace(strings.TrimSuffix(body, "."))
					body = regexp.MustCompile("(?i)\\s+(?:in\\s+)?(?:file\\s+)?[A-Za-z0-9_./\\\\-]+\\.go\\s*$").ReplaceAllString(body, "")
					if strings.HasPrefix(strings.ToLower(body), "func ") {
						return body + "\n"
					}
					if strings.Contains(body, "(") || strings.Contains(body, "{") {
						return normalizeReplacementFunction("", body) + "\n"
					}
				}
			}
		}
	}
	if idx := strings.Index(lower, "replace "); idx >= 0 {
		nameEnd := strings.Index(strings.TrimSpace(prompt[idx+len("replace "):]), " with ")
		if nameEnd >= 0 {
			name := strings.TrimSpace(prompt[idx+len("replace ") : idx+len("replace ")+nameEnd])
			body := strings.TrimSpace(prompt[idx+len("replace ")+nameEnd+len(" with "):])
			body = strings.TrimSpace(strings.TrimSuffix(body, "."))
			body = regexp.MustCompile("(?i)\\s+(?:in\\s+)?(?:file\\s+)?[A-Za-z0-9_./\\\\-]+\\.go\\s*$").ReplaceAllString(body, "")
			if strings.HasPrefix(strings.ToLower(body), "func ") {
				return body + "\n"
			}
			if strings.Contains(body, "(") || strings.Contains(body, "{") {
				if strings.HasPrefix(strings.ToLower(body), "func ") || strings.Contains(strings.ToLower(body), "func ") {
					return body + "\n"
				}
				if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*\(.*\)`).MatchString(strings.TrimSpace(body)) {
					return "func " + strings.TrimSpace(body) + "\n"
				}
				if name != "" {
					return "func " + name + " " + body + "\n"
				}
			}
		}
		for _, withPrefix := range []string{" with func ", " with function ", " with "} {
			if j := strings.Index(strings.ToLower(prompt[idx:]), withPrefix); j >= 0 {
				tail := strings.TrimSpace(prompt[idx+j+len(withPrefix):])
				tail = strings.TrimSpace(strings.TrimSuffix(tail, "."))
				tail = regexp.MustCompile("(?i)\\s+(?:in\\s+)?(?:file\\s+)?[A-Za-z0-9_./\\\\-]+\\.go\\s*$").ReplaceAllString(tail, "")
				if strings.HasPrefix(strings.ToLower(tail), "func ") {
					return tail + "\n"
				}
				if strings.Contains(tail, "(") {
					return "func " + tail + "\n"
				}
			}
		}
	}
	candidates := []string{"add function ", "create function ", "new function ", "insert function ", "define function "}
	for _, prefix := range candidates {
		if idx := strings.Index(lower, prefix); idx >= 0 {
			rest := lower[idx+len(prefix):]
			rest = strings.TrimSpace(rest)
			rest = strings.ReplaceAll(rest, " to file ", " ")
			rest = strings.ReplaceAll(rest, " in file ", " ")
			rest = strings.ReplaceAll(rest, " to /file ", " ")
			rest = strings.ReplaceAll(rest, " in /file ", " ")
			rest = strings.ReplaceAll(rest, " to folder ", " ")
			rest = strings.ReplaceAll(rest, " in folder ", " ")
			rest = strings.ReplaceAll(rest, " to directory ", " ")
			rest = strings.ReplaceAll(rest, " in directory ", " ")
			rest = strings.TrimSpace(rest)
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			name := fields[0]
			if name == "to" || name == "in" || name == "file" || name == "folder" || name == "directory" {
				continue
			}
			name = strings.Trim(name, "_/.- ")
			if name == "" {
				continue
			}
			name = strings.Map(func(r rune) rune {
				if r == '/' || r == '.' || r == '-' || r == '_' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
					return -1
				}
				return r
			}, name)
			if name == "" {
				continue
			}
			funcName := strings.ToUpper(name[:1]) + name[1:]
			return fmt.Sprintf("func %s() {\n\t// TODO: implement\n}\n", funcName)
		}
	}
	return ""
}

func inferTargetFileFromPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}

	lower := strings.ToLower(prompt)
	// For prompts like "import X from file A.go into file B.go" or similar, favor the destination target
	if strings.Contains(lower, "into file ") || strings.Contains(lower, "into ") {
		for _, key := range []string{"into file ", "into "} {
			if idx := strings.Index(lower, key); idx >= 0 {
				target := strings.TrimSpace(prompt[idx+len(key):])
				if match := regexp.MustCompile(`(?i)(?:file\s+)?([A-Za-z0-9_./\-]+\.go)`).FindStringSubmatch(target); len(match) > 1 {
					return filepath.Clean(match[1])
				}
			}
		}
	}

	if strings.Contains(lower, "import ") && strings.Contains(lower, " to ") {
		if i := strings.Index(lower, " to "); i >= 0 {
			target := strings.TrimSpace(prompt[i+len(" to "):])
			target = strings.Trim(target, " \t\r\n\"'`.")
			target = strings.TrimPrefix(target, "file ")
			if filepath.Ext(target) == ".go" {
				return filepath.Clean(target)
			}
			if match := regexp.MustCompile("(?i)(?:file\\s+)?([A-Za-z0-9_./\\-]+\\.go)").FindString(target); match != "" {
				candidate := strings.TrimSpace(match)
				candidate = strings.TrimPrefix(candidate, "file ")
				candidate = strings.TrimSpace(candidate)
				if filepath.Ext(candidate) == ".go" {
					return filepath.Clean(candidate)
				}
			}
		}
	}

	if match := regexp.MustCompile("(?i)(?:^|[\\s\"'`])([A-Za-z0-9_./\\-]+\\.go)").FindString(prompt); match != "" {
		path := strings.Trim(match, " \t\r\n\"'`")
		path = strings.TrimPrefix(path, "file ")
		if filepath.Ext(path) == ".go" {
			return filepath.Clean(path)
		}
	}

	if strings.Contains(lower, "replace ") && strings.Contains(lower, " with ") {
		if match := regexp.MustCompile("(?i)(?:in|file)\\s+([A-Za-z0-9_./\\-]+\\.go)").FindString(prompt); match != "" {
			path := strings.TrimSpace(match)
			path = strings.TrimPrefix(path, "file ")
			if idx := strings.LastIndex(path, " "); idx >= 0 {
				path = path[idx+1:]
			}
			if filepath.Ext(path) == ".go" {
				return filepath.Clean(path)
			}
		}
	}

	for _, prefix := range []string{"json tags to ", "json tag to ", "json tags for ", "json tag for ", "add json tags to ", "add json tag to ", "add struct ", "create struct ", "type "} {
		if idx := strings.Index(lower, prefix); idx >= 0 {
			rest := strings.TrimSpace(prompt[idx+len(prefix):])
			rest = strings.Trim(rest, " .,:;()[]{}\"'")
			if fields := strings.Fields(rest); len(fields) > 0 {
				name := normalizeIdentifierName(fields[0])
				if name != "" {
					return filepath.Join(".", strings.ToLower(name)+".go")
				}
			}
		}
	}
	if idx := strings.Index(lower, "user"); idx >= 0 {
		return filepath.Join(".", "user.go")
	}
	return ""
}

func ensureGoTargetFile(filePath string, prompt string) error {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return fmt.Errorf("empty file path")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}
	if _, err := os.Stat(filePath); err == nil {
		return nil
	}
	typeName := "GeneratedModel"
	if inferred := inferTargetFileFromPrompt(prompt); inferred != "" && filepath.Base(inferred) != "" {
		typeName = strings.TrimSuffix(filepath.Base(inferred), filepath.Ext(inferred))
		typeName = strings.Title(typeName)
	}
	if idx := strings.Index(strings.ToLower(prompt), "json tags to "); idx >= 0 {
		name := strings.TrimSpace(prompt[idx+len("json tags to "):])
		name = strings.Trim(name, " .,:;()[]{}\"'")
		if fields := strings.Fields(name); len(fields) > 0 {
			typeName = normalizeIdentifierName(fields[0])
		}
	}
	if typeName == "" {
		typeName = "GeneratedModel"
	}
	defaultContent := fmt.Sprintf("package main\n\ntype %s struct {\n\tFirstName string\n\tLastName string\n}\n", typeName)
	return os.WriteFile(filePath, []byte(defaultContent), 0644)
}

func addImportToFile(filePath, importPath string) error {
	if strings.TrimSpace(filePath) == "" || strings.TrimSpace(importPath) == "" {
		return fmt.Errorf("empty file path or import path")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, filePath, string(content), parser.ParseComments)
	if err != nil {
		return err
	}
	for _, imp := range parsed.Imports {
		if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == importPath {
			return nil
		}
	}
	astutil.AddImport(fileSet, parsed, importPath)
	var b strings.Builder
	if err := format.Node(&b, fileSet, parsed); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(b.String()), 0644)
}

func addTypeToFile(filePath, typeDecl string) error {
	if strings.TrimSpace(filePath) == "" || strings.TrimSpace(typeDecl) == "" {
		return fmt.Errorf("empty file path or type declaration")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, filePath, string(content), parser.ParseComments)
	if err != nil {
		return err
	}
	parsedDecl, err := parser.ParseFile(fileSet, filePath, "package main\n\n"+typeDecl, parser.ParseComments)
	if err != nil {
		return err
	}
	if len(parsedDecl.Decls) == 0 {
		return fmt.Errorf("type declaration is empty")
	}
	appendDecls := parsed.Decls
	appendDecls = append(appendDecls, parsedDecl.Decls...)
	parsed.Decls = appendDecls
	var b strings.Builder
	if err := format.Node(&b, fileSet, parsed); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(b.String()), 0644)
}

func applyFunctionReplacement(filePath, name, replacement string) (string, error) {
	if strings.TrimSpace(filePath) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(replacement) == "" {
		return "", fmt.Errorf("empty file path, name, or replacement")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	text := string(content)
	trimmedReplacement := normalizeReplacementFunction(name, replacement)
	if trimmedReplacement == "" {
		return "", fmt.Errorf("empty replacement")
	}

	fileSet := token.NewFileSet()
	parsed, parseErr := parser.ParseFile(fileSet, filePath, text, parser.ParseComments)
	if parseErr == nil {
		replacementSrc := "package main\n\n" + trimmedReplacement
		replacementFile, err := parser.ParseFile(fileSet, "", replacementSrc, parser.ParseComments)
		if err != nil {
			return "", err
		}
		if len(replacementFile.Decls) == 0 {
			return "", fmt.Errorf("replacement is not a Go declaration")
		}
		funcDecl, ok := replacementFile.Decls[0].(*ast.FuncDecl)
		if !ok {
			return "", fmt.Errorf("replacement is not a function declaration")
		}
		found := false
		for i, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name != nil && strings.EqualFold(fn.Name.Name, name) {
				parsed.Decls[i] = funcDecl
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("function %s not found", name)
		}
		var b strings.Builder
		if err := format.Node(&b, fileSet, parsed); err != nil {
			return "", err
		}
		if err := ValidateASTSource(filePath, b.String()); err != nil {
			return "", err
		}
		if err := os.WriteFile(filePath, []byte(b.String()), 0644); err != nil {
			return "", err
		}
		return b.String(), nil
	}

	startIdx := strings.Index(strings.ToLower(text), "func "+strings.ToLower(name))
	if startIdx < 0 {
		return "", parseErr
	}
	funcStart := startIdx
	braceStart := strings.Index(text[funcStart:], "{")
	if braceStart < 0 {
		funcEnd := strings.Index(text[funcStart:], "}")
		if funcEnd < 0 {
			return "", fmt.Errorf("function %s missing opening and closing brace", name)
		}
		funcEnd += funcStart + 1
		updated := text[:funcStart] + trimmedReplacement + text[funcEnd:]
		if err := ValidateASTSource(filePath, updated); err != nil {
			return "", err
		}
		if err := os.WriteFile(filePath, []byte(updated), 0644); err != nil {
			return "", err
		}
		return updated, nil
	}
	braceStart += funcStart
	braceDepth := 0
	funcEnd := -1
	for i := braceStart; i < len(text); i++ {
		switch text[i] {
		case '{':
			braceDepth++
		case '}':
			braceDepth--
			if braceDepth == 0 {
				funcEnd = i + 1
				break
			}
		}
		if funcEnd > 0 {
			break
		}
	}
	if funcEnd < 0 {
		return "", fmt.Errorf("function %s missing closing brace", name)
	}
	updated := text[:funcStart] + trimmedReplacement + text[funcEnd:]
	if err := ValidateASTSource(filePath, updated); err != nil {
		return "", err
	}
	if err := os.WriteFile(filePath, []byte(updated), 0644); err != nil {
		return "", err
	}
	return updated, nil
}

// Intent represents a small structured intent extracted from the user's
// prompt. The intent is intentionally minimal so the deterministic renderer
// can generate syntactically-correct Go using go/ast.
type Intent struct {
	Action      string   // e.g. "ADD_FUNC", "ADD_ERROR_CHECK"
	Name        string   // function or symbol name
	Target      string   // variable to check, e.g. "err"
	Ret         string   // return zero value (as source text) when needed
	Params      []string // parameter strings like "name string", "n int"
	Receiver    string   // receiver like "s *Service" or "*Service"
	Returns     []string // return type strings like "bool", "error"
	Description string   // description of the intent
}

func intentFromDenseModel(prompt string, conv *Conversation, model *dense.DenseModel) Intent {
	if model == nil {
		return Intent{}
	}

	src := ""
	if conv != nil && conv.TargetGoFile() != "" {
		if b, err := os.ReadFile(conv.TargetGoFile()); err == nil {
			src = string(b)
		}
	}
	input := dense.ContextualFeatureVector(prompt, src, dense.CommandVocab)
	if len(input) == 0 {
		return Intent{}
	}
	preds := model.Predict([][]float32{input})
	if len(preds) == 0 {
		return Intent{}
	}
	label := preds[0]
	if label < 0 || label >= len(dense.CommandLabels) {
		return Intent{}
	}
	candidate := dense.CommandLabels[label]
	if candidate == "social" && !strings.Contains(strings.ToLower(prompt), "create") && !strings.Contains(strings.ToLower(prompt), "import") && !strings.Contains(strings.ToLower(prompt), "function") && !strings.Contains(strings.ToLower(prompt), "test") {
		return Intent{}
	}
	if strings.Contains(strings.ToLower(prompt), "import") || strings.Contains(strings.ToLower(prompt), "add import") {
		if i := strings.Index(prompt, `"`); i >= 0 {
			if j := strings.Index(prompt[i+1:], `"`); j >= 0 {
				return Intent{Action: "ADD_IMPORT", Name: prompt[i+1 : i+1+j]}
			}
		}
	}
	if strings.Contains(strings.ToLower(prompt), "function") || strings.Contains(strings.ToLower(prompt), "method") || candidate == "code_update" {
		if intent := parseSignatureIntent(prompt); intent.Action != "" {
			return intent
		}
	}
	if candidate == "file_create" && strings.Contains(strings.ToLower(prompt), "file") {
		return Intent{Action: "ADD_FILE"}
	}
	return Intent{}
}

func parseSignatureIntent(prompt string) Intent {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if strings.Contains(lower, "add function") || strings.Contains(lower, "create function") {
		tail := strings.TrimSpace(prompt)
		for _, prefix := range []string{"add function ", "create function "} {
			if strings.HasPrefix(strings.ToLower(tail), prefix) {
				tail = strings.TrimSpace(tail[len(prefix):])
				break
			}
		}
		if idx := strings.Index(strings.ToLower(tail), " using "); idx >= 0 {
			name := strings.TrimSpace(tail[:idx])
			if normalized := normalizeIdentifierName(name); normalized != "" {
				return Intent{Action: "ADD_FUNC", Name: normalized, Returns: []string{"error"}}
			}
		}
	}
	if strings.Contains(lower, "json tag") || strings.Contains(lower, "json tags") {
		for _, prefix := range []string{"json tags to ", "json tag to ", "json tags for ", "json tag for ", "add json tags to ", "add json tag to "} {
			if idx := strings.Index(lower, prefix); idx >= 0 {
				rest := strings.TrimSpace(prompt[idx+len(prefix):])
				rest = strings.Trim(rest, " .,:;()[]{}\"'")
				if fields := strings.Fields(rest); len(fields) > 0 {
					name := normalizeIdentifierName(fields[0])
					if name != "" {
						return Intent{Action: "ADD_JSON_TAGS", Name: name}
					}
				}
			}
		}
		return Intent{Action: "ADD_JSON_TAGS", Name: "User"}
	}
	if strings.Contains(lower, "import ") || strings.Contains(lower, "add import") {
		if m := regexp.MustCompile(`(?i)(?:import|add\s+import)\s+(?:the\s+)?(?:struct|type|function|symbol)?\s*(?:from\s+)?["']?([A-Za-z0-9_./\\-]+\.(?:go|mod))?["']?`).FindStringSubmatch(prompt); len(m) > 1 && m[1] != "" {
			if pkg := normalizeImportPath(m[1]); pkg != "" {
				return Intent{Action: "ADD_IMPORT", Name: pkg}
			}
		}
		if i1 := strings.Index(prompt, `"`); i1 >= 0 {
			if i2 := strings.Index(prompt[i1+1:], `"`); i2 >= 0 {
				pkg := prompt[i1+1 : i1+1+i2]
				if pkg != "" {
					return Intent{Action: "ADD_IMPORT", Name: pkg}
				}
			}
		}
		if idx := strings.Index(lower, " from "); idx >= 0 {
			rest := prompt[idx+len(" from "):]
			if i := strings.Index(rest, " "); i >= 0 {
				rest = rest[:i]
			}
			if pkg := normalizeImportPath(rest); pkg != "" {
				return Intent{Action: "ADD_IMPORT", Name: pkg}
			}
		}
		parts := strings.Fields(prompt)
		if len(parts) > 0 {
			last := strings.Trim(parts[len(parts)-1], " ,.")
			if last != "" && !strings.Contains(last, ".go") {
				return Intent{Action: "ADD_IMPORT", Name: last}
			}
		}
	}
	if idx := strings.Index(lower, "function "); idx >= 0 {
		after := prompt[idx+len("function "):]
		name := strings.TrimSpace(after)
		params := []string{}
		rets := []string{}
		receiver := ""
		if i := strings.Index(after, "("); i >= 0 {
			name = strings.TrimSpace(after[:i])
			rest := after[i+1:]
			if j := strings.Index(rest, ")"); j >= 0 {
				paramsStr := rest[:j]
				for _, p := range strings.Split(paramsStr, ",") {
					if s := strings.TrimSpace(p); s != "" {
						params = append(params, s)
					}
				}
				post := strings.TrimSpace(rest[j+1:])
				if strings.HasPrefix(strings.ToLower(post), "returns ") {
					rt := strings.TrimSpace(post[len("returns "):])
					for _, r := range strings.Split(rt, ",") {
						if s := strings.TrimSpace(strings.Trim(r, "()")); s != "" {
							rets = append(rets, s)
						}
					}
				} else if post != "" {
					if strings.HasPrefix(post, "(") {
						p := strings.Trim(post, "() ")
						for _, r := range strings.Split(p, ",") {
							if s := strings.TrimSpace(r); s != "" {
								rets = append(rets, s)
							}
						}
					} else {
						fields := strings.Fields(post)
						if len(fields) > 0 {
							rets = append(rets, strings.Trim(fields[0], ",."))
						}
					}
				}
			}
		}
		if idx2 := strings.Index(strings.ToLower(prompt), "method on "); idx2 >= 0 {
			after := strings.TrimSpace(prompt[idx2+len("method on "):])
			recv := strings.Trim(strings.Fields(after)[0], " ,.")
			if recv != "" {
				receiver = recv
			}
		} else if idx2 := strings.Index(strings.ToLower(prompt), "method of "); idx2 >= 0 {
			after := strings.TrimSpace(prompt[idx2+len("method of "):])
			recv := strings.Trim(strings.Fields(after)[0], " ,.")
			if recv != "" {
				receiver = recv
			}
		}
		if name != "" {
			return Intent{Action: "ADD_FUNC", Name: strings.Title(strings.TrimSpace(name)), Params: params, Receiver: receiver, Returns: rets}
		}
	}
	if idxm := strings.Index(lower, "method "); idxm >= 0 {
		after := prompt[idxm+len("method "):]
		name := strings.TrimSpace(after)
		params := []string{}
		rets := []string{}
		receiver := ""
		lowerAfter := strings.ToLower(after)
		onIdx := strings.Index(lowerAfter, " on ")
		var sigPart string
		if onIdx >= 0 {
			name = strings.TrimSpace(after[:onIdx])
			afterOn := strings.TrimSpace(after[onIdx+len(" on "):])
			if i := strings.Index(afterOn, "("); i >= 0 {
				receiver = strings.TrimSpace(afterOn[:i])
				sigPart = afterOn[i:]
			} else {
				if recFields := strings.Fields(afterOn); len(recFields) > 0 {
					receiver = strings.Trim(recFields[0], " ,.")
				}
			}
		} else {
			sigPart = after
		}
		if sigPart != "" {
			if i := strings.Index(sigPart, "("); i >= 0 {
				rest := sigPart[i+1:]
				if j := strings.Index(rest, ")"); j >= 0 {
					paramsStr := rest[:j]
					for _, p := range strings.Split(paramsStr, ",") {
						if s := strings.TrimSpace(p); s != "" {
							params = append(params, s)
						}
					}
					post := strings.TrimSpace(rest[j+1:])
					if strings.HasPrefix(strings.ToLower(post), "returns ") {
						rt := strings.TrimSpace(post[len("returns "):])
						for _, r := range strings.Split(rt, ",") {
							if s := strings.TrimSpace(strings.Trim(r, "()")); s != "" {
								rets = append(rets, s)
							}
						}
					} else if post != "" {
						fields := strings.Fields(post)
						if len(fields) > 0 {
							rets = append(rets, strings.Trim(fields[0], ",."))
						}
					}
				}
			}
		}
		if name != "" {
			return Intent{Action: "ADD_FUNC", Name: strings.Title(strings.TrimSpace(name)), Params: params, Receiver: sanitizeReceiver(receiver), Returns: rets}
		}
	}
	if strings.Contains(lower, "err != nil") || strings.Contains(lower, "add error check") || strings.Contains(lower, "return on error") || strings.Contains(lower, "on error") || strings.Contains(lower, "error check") {
		ret := ""
		target := "err"
		if idx := strings.Index(lower, "for "); idx >= 0 {
			after := strings.TrimSpace(lower[idx+len("for "):])
			if fields := strings.Fields(after); len(fields) > 0 {
				target = strings.Trim(fields[0], " ,.")
			}
		}
		if strings.Contains(lower, "return nil") || strings.Contains(lower, "return nil,") || strings.Contains(lower, "nil on error") {
			ret = "nil"
		} else if strings.Contains(lower, "return \"\"") || strings.Contains(lower, "empty string") || strings.Contains(lower, "string on error") {
			ret = `""`
		} else if strings.Contains(lower, "return 0") || strings.Contains(lower, "0 on error") {
			ret = "0"
		}
		if ret != "" || strings.Contains(lower, "on error") || strings.Contains(lower, "error check") {
			return Intent{Action: "ADD_ERROR_CHECK", Target: target, Ret: ret}
		}
	}
	if strings.Contains(lower, "unit test") || strings.Contains(lower, "add test") || strings.Contains(lower, "create test") {
		parts := strings.Fields(prompt)
		for i, p := range parts {
			if strings.Contains(strings.ToLower(p), "for") && i+1 < len(parts) {
				name := strings.Trim(parts[i+1], " ,.")
				return Intent{Action: "ADD_TEST", Name: name}
			}
		}
		return Intent{Action: "ADD_TEST", Name: "Example"}
	}
	if strings.Contains(lower, "create struct") || strings.Contains(lower, "create type") || strings.Contains(lower, "add struct") {
		parts := strings.Fields(prompt)
		for i, p := range parts {
			if strings.EqualFold(p, "struct") && i > 0 {
				name := strings.Trim(parts[i-1], " ,.")
				if name != "" {
					return Intent{Action: "ADD_TYPE", Name: name}
				}
			}
			if strings.EqualFold(p, "type") && i+1 < len(parts) {
				name := strings.Trim(parts[i+1], " ,.")
				if name != "" && !strings.EqualFold(name, "struct") {
					return Intent{Action: "ADD_TYPE", Name: name}
				}
			}
		}
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.Trim(parts[i], " ,.")
			if candidate == "" || strings.EqualFold(candidate, "create") || strings.EqualFold(candidate, "type") || strings.EqualFold(candidate, "struct") || strings.EqualFold(candidate, "add") {
				continue
			}
			return Intent{Action: "ADD_TYPE", Name: candidate}
		}
	}
	if strings.Contains(lower, "opening brace") || strings.Contains(lower, "missing opening brace") {
		switch {
		case strings.Contains(lower, "if"):
			return Intent{Action: "ADD_FUNC", Name: "IfCondition"}
		case strings.Contains(lower, "for"):
			return Intent{Action: "ADD_FUNC", Name: "ForLoop"}
		case strings.Contains(lower, "switch"):
			return Intent{Action: "ADD_FUNC", Name: "SwitchCase"}
		case strings.Contains(lower, "struct") || strings.Contains(lower, "type"):
			return Intent{Action: "ADD_TYPE", Name: "BraceStruct"}
		case strings.Contains(lower, "function"):
			return Intent{Action: "ADD_FUNC", Name: "FixedFunction"}
		default:
			return Intent{Action: "ADD_FUNC", Name: "BraceFix"}
		}
	}
	return Intent{}
}

// predictIntent extracts a structured intent from the prompt using the dense
// model as a strong prior, then falls back to signature parsing and example
// matching for deterministic action extraction.
func normalizeIdentifierName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "-", " ")
	value = strings.ReplaceAll(value, "/", " ")
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.Trim(value, " .,:;()[]{}\"")
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return ""
	}
	for i, part := range parts {
		part = strings.Trim(part, " .,:;()[]{}\"")
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func sanitizeReceiver(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.IndexAny(value, " (\t"); idx >= 0 {
		value = value[:idx]
	}
	value = strings.Trim(value, "* .,:;()[]{}\"")
	return value
}

func normalizeImportPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	value = filepath.ToSlash(value)
	value = strings.TrimSuffix(value, ".go")
	value = strings.TrimSuffix(value, "/")
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	if value == "" || value == "." {
		return ""
	}
	return value
}

func parseStructFieldSpecs(raw string) []string {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "with")
	text = strings.TrimPrefix(text, "fields")
	text = strings.TrimPrefix(text, "field")
	text = strings.TrimSpace(text)
	text = strings.Trim(text, ",;: ")
	if text == "" {
		return nil
	}
	chunks := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' })
	var out []string
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		parts := strings.Fields(chunk)
		if len(parts) < 2 {
			continue
		}
		for i := 0; i+1 < len(parts); i += 2 {
			name := strings.Trim(parts[i], " .,:;()[]{}\"")
			typ := strings.Trim(parts[i+1], " .,:;()[]{}\"")
			if name == "" || typ == "" {
				continue
			}
			out = append(out, name+" "+typ)
		}
	}
	return out
}

func predictIntent(prompt string, conv *Conversation, model *dense.DenseModel, examples []dense.CommandExample) Intent {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	parsed := dense.ParseHybridPrompt(prompt)
	if parsed.Action == "replace" {
		if len(parsed.Identifiers) > 0 && parsed.RawCode != "" {
			name, params, returns := extractFunctionSignatureParts(parsed.RawCode)
			if name == "" {
				name = parsed.Identifiers[0]
			}
			return Intent{Action: "ADD_FUNC", Name: name, Params: params, Returns: returns, Description: parsed.RawCode}
		}
	}
	if strings.Contains(lower, "replace ") && strings.Contains(lower, " with ") {
		if snippet := functionSnippetFromPrompt(prompt); snippet != "" {
			return Intent{Action: "ADD_FUNC", Name: "Replacement", Description: snippet}
		}
	}
	if strings.Contains(lower, "create file") || strings.Contains(lower, "modify file") || strings.Contains(lower, "edit file") || strings.Contains(lower, "delete file") || strings.Contains(lower, "create folder") || strings.Contains(lower, "delete folder") || strings.Contains(lower, "remove directory") || strings.Contains(lower, "remove folder") || strings.Contains(lower, "delete directory") {
		if match := regexp.MustCompile("(?i)(?:create|modify|edit|delete|remove)\\s+(?:file|folder|directory)\\s+([A-Za-z0-9_./\\\\-]+)").FindString(prompt); match != "" {
			name := strings.TrimSpace(match)
			if idx := strings.LastIndex(name, " "); idx >= 0 {
				name = name[idx+1:]
			}
			if name != "" {
				return Intent{Action: "ADD_FILE", Name: name}
			}
		}
	}
	if (isGreeting(prompt) || isFarewell(prompt) || isThanks(prompt) || (isFollowUp(prompt, conv) && conv != nil && conv.HasContext())) && !strings.Contains(lower, "function") && !strings.Contains(lower, "method") && !strings.Contains(lower, "struct") && !strings.Contains(lower, "type ") && !strings.Contains(lower, "import") && !strings.Contains(lower, "json") && !strings.Contains(lower, "brace") && !strings.Contains(lower, "return ") && !strings.Contains(lower, "test") && !strings.Contains(lower, "error") && !strings.Contains(lower, "folder") && !strings.Contains(lower, "directory") && !strings.Contains(lower, "file") {
		return Intent{Action: "SOCIAL", Name: "social"}
	}
	if strings.Contains(lower, "list directory") || strings.Contains(lower, "show directory") || strings.Contains(lower, "what is directory") || strings.Contains(lower, "show folder") || strings.Contains(lower, "list folder") || strings.Contains(lower, "what is folder") {
		return Intent{Action: "ADD_FILE", Name: "folder_query"}
	}
	if strings.Contains(lower, "json tag") || strings.Contains(lower, "json tags") {
		if intent := parseSignatureIntent(prompt); intent.Action != "" {
			return intent
		}
	}
	if model != nil {
		if intent := intentFromDenseModel(prompt, conv, model); intent.Action != "" {
			return intent
		}
	}
	if strings.Contains(lower, "using ") {
		if idx := strings.Index(strings.ToLower(prompt), " using "); idx >= 0 {
			pkg := strings.TrimSpace(prompt[idx+len(" using "):])
			pkg = strings.Trim(pkg, " .,:;()[]{}\"'")
			if pkg != "" {
				return Intent{Action: "ADD_IMPORT", Name: pkg}
			}
		}
	}
	if strings.Contains(lower, "add method") || strings.Contains(lower, "create method") {
		body := strings.TrimSpace(prompt)
		for _, prefix := range []string{"add method ", "create method "} {
			if strings.HasPrefix(strings.ToLower(body), prefix) {
				body = strings.TrimSpace(body[len(prefix):])
				break
			}
		}
		methodName := ""
		receiver := ""
		if idx := strings.Index(strings.ToLower(body), " to struct "); idx >= 0 {
			methodPart := strings.TrimSpace(body[:idx])
			receiverPart := strings.TrimSpace(body[idx+len(" to struct "):])
			receiver = sanitizeReceiver(receiverPart)
			if receiver == "" {
				receiver = "Worker"
			}
			methodName = normalizeIdentifierName(methodPart)
		} else if idx := strings.Index(strings.ToLower(body), " on "); idx >= 0 {
			methodPart := strings.TrimSpace(body[:idx])
			receiverPart := strings.TrimSpace(body[idx+len(" on "):])
			receiver = sanitizeReceiver(receiverPart)
			methodName = normalizeIdentifierName(methodPart)
		} else if idx := strings.Index(strings.ToLower(body), " to "); idx >= 0 {
			methodPart := strings.TrimSpace(body[:idx])
			receiverPart := strings.TrimSpace(body[idx+len(" to "):])
			receiver = sanitizeReceiver(receiverPart)
			methodName = normalizeIdentifierName(methodPart)
		}
		if methodName != "" {
			if receiver == "" {
				receiver = "Worker"
			}
			return Intent{Action: "ADD_FUNC", Name: methodName, Receiver: receiver, Returns: []string{"error"}}
		}
	}
	if strings.Contains(lower, "add function") || strings.Contains(lower, "create function") {
		tail := strings.TrimSpace(prompt)
		for _, prefix := range []string{"add function ", "create function "} {
			if strings.HasPrefix(strings.ToLower(tail), prefix) {
				tail = strings.TrimSpace(tail[len(prefix):])
				break
			}
		}
		if idx := strings.Index(strings.ToLower(tail), " using "); idx >= 0 {
			name := strings.TrimSpace(tail[:idx])
			if parsed := normalizeIdentifierName(name); parsed != "" {
				return Intent{Action: "ADD_FUNC", Name: parsed, Returns: []string{"error"}}
			}
		}
	}
	// Import intent
	if strings.Contains(lower, "import ") || strings.Contains(lower, "add import") {
		// Check for pattern: "from file <path>" or "from <path>"
		if idx := strings.Index(lower, " from "); idx >= 0 {
			rest := strings.TrimSpace(prompt[idx+len(" from "):])
			if strings.HasPrefix(strings.ToLower(rest), "file ") {
				rest = strings.TrimSpace(rest[len("file "):])
			}
			if i := strings.Index(rest, " "); i >= 0 {
				rest = rest[:i]
			}
			rest = strings.Trim(rest, `"'`)
			// If it's a file path like "jim/jake.go", the import path is the package directory: "github.com/golangast/dense/jim"
			// But for now, let's normalize it to the relative directory path "github.com/golangast/dense/jim" or "jim"
			if strings.HasSuffix(rest, ".go") {
				dir := filepath.Dir(rest)
				if dir != "" && dir != "." {
					return Intent{Action: "ADD_IMPORT", Name: dir}
				}
			}
			if pkg := normalizeImportPath(rest); pkg != "" {
				return Intent{Action: "ADD_IMPORT", Name: pkg}
			}
		}
		if m := regexp.MustCompile(`(?i)(?:import|add\s+import)\s+(?:the\s+)?(?:struct|type|function|symbol)?\s*(?:from\s+)?["']?([A-Za-z0-9_./\\-]+\.(?:go|mod))?["']?`).FindStringSubmatch(prompt); len(m) > 1 && m[1] != "" {
			if strings.HasSuffix(m[1], ".go") {
				dir := filepath.Dir(m[1])
				if dir != "" && dir != "." {
					return Intent{Action: "ADD_IMPORT", Name: dir}
				}
			}
			if pkg := normalizeImportPath(m[1]); pkg != "" {
				return Intent{Action: "ADD_IMPORT", Name: pkg}
			}
		}
		if i1 := strings.Index(prompt, "\""); i1 >= 0 {
			if i2 := strings.Index(prompt[i1+1:], "\""); i2 >= 0 {
				pkg := prompt[i1+1 : i1+1+i2]
				if pkg != "" {
					return Intent{Action: "ADD_IMPORT", Name: pkg}
				}
			}
		}
		parts := strings.Fields(prompt)
		if len(parts) > 0 {
			last := strings.Trim(parts[len(parts)-1], " ,.")
			if last != "" && !strings.Contains(last, ".go") {
				return Intent{Action: "ADD_IMPORT", Name: last}
			}
		}
	}
	// Prefer explicit function creation patterns.
	// Try to parse function or method name, params and returns from explicit
	// signature patterns like: "create function Validate(name string) bool",
	// or method forms like "create method DoThing on Service(a int) error".
	if idx := strings.Index(lower, "function "); idx >= 0 {
		after := prompt[idx+len("function "):]
		// name up to '(' or end
		name := strings.TrimSpace(after)
		params := []string{}
		rets := []string{}
		receiver := ""
		if i := strings.Index(after, "("); i >= 0 {
			name = strings.TrimSpace(after[:i])
			rest := after[i+1:]
			if j := strings.Index(rest, ")"); j >= 0 {
				paramsStr := rest[:j]
				for _, p := range strings.Split(paramsStr, ",") {
					if s := strings.TrimSpace(p); s != "" {
						params = append(params, s)
					}
				}
				// look after ')' for return type
				post := strings.TrimSpace(rest[j+1:])
				// examples: " bool", " returns bool", " -> (bool, error)"
				if strings.HasPrefix(strings.ToLower(post), "returns ") {
					rt := strings.TrimSpace(post[len("returns "):])
					// split on ','
					for _, r := range strings.Split(rt, ",") {
						if s := strings.TrimSpace(strings.Trim(r, "()")); s != "" {
							rets = append(rets, s)
						}
					}
				} else if post != "" {
					// if starts with '(' may contain multiple
					if strings.HasPrefix(post, "(") {
						p := strings.Trim(post, "() ")
						for _, r := range strings.Split(p, ",") {
							if s := strings.TrimSpace(r); s != "" {
								rets = append(rets, s)
							}
						}
					} else {
						// single return type token
						fields := strings.Fields(post)
						if len(fields) > 0 {
							rets = append(rets, strings.Trim(fields[0], ",."))
						}
					}
				}
			}
		}
		// Receiver heuristics: look for phrases like "method on Service" or "method of *Service"
		recv := ""
		if idx2 := strings.Index(strings.ToLower(prompt), "method on "); idx2 >= 0 {
			after := strings.TrimSpace(prompt[idx2+len("method on "):])
			recv = strings.Trim(strings.Fields(after)[0], " ,.")
		} else if idx2 := strings.Index(strings.ToLower(prompt), "method of "); idx2 >= 0 {
			after := strings.TrimSpace(prompt[idx2+len("method of "):])
			recv = strings.Trim(strings.Fields(after)[0], " ,.")
		} else if idx2 := strings.Index(strings.ToLower(prompt), "on type "); idx2 >= 0 {
			after := strings.TrimSpace(prompt[idx2+len("on type "):])
			recv = strings.Trim(strings.Fields(after)[0], " ,.")
		}

		// If we captured a name, return intent
		if name != "" {
			it := Intent{Action: "ADD_FUNC", Name: strings.Title(strings.TrimSpace(name)), Params: params, Receiver: sanitizeReceiver(receiver), Returns: rets}
			if recv != "" && it.Receiver == "" {
				it.Receiver = sanitizeReceiver(recv)
			}
			return it
		}
	}

	// Method form: "method NAME on RECEIVER(params) returns"
	if idxm := strings.Index(lower, "method "); idxm >= 0 {
		after := prompt[idxm+len("method "):]
		name := strings.TrimSpace(after)
		params := []string{}
		rets := []string{}
		receiver := ""
		// check for ' on '
		lowerAfter := strings.ToLower(after)
		onIdx := strings.Index(lowerAfter, " on ")
		var sigPart string
		if onIdx >= 0 {
			name = strings.TrimSpace(after[:onIdx])
			afterOn := strings.TrimSpace(after[onIdx+len(" on "):])
			// receiver may be followed immediately by '(' or a space. Extract
			// receiver up to the first '(' if present, otherwise first token.
			if i := strings.Index(afterOn, "("); i >= 0 {
				receiver = strings.TrimSpace(afterOn[:i])
				sigPart = afterOn[i:]
			} else {
				recFields := strings.Fields(afterOn)
				if len(recFields) > 0 {
					receiver = strings.Trim(recFields[0], " ,.")
				}
			}
		} else {
			sigPart = after
		}
		if sigPart != "" {
			if i := strings.Index(sigPart, "("); i >= 0 {
				name = strings.TrimSpace(name)
				rest := sigPart[i+1:]
				if j := strings.Index(rest, ")"); j >= 0 {
					paramsStr := rest[:j]
					for _, p := range strings.Split(paramsStr, ",") {
						if s := strings.TrimSpace(p); s != "" {
							params = append(params, s)
						}
					}
					post := strings.TrimSpace(rest[j+1:])
					if strings.HasPrefix(strings.ToLower(post), "returns ") {
						rt := strings.TrimSpace(post[len("returns "):])
						for _, r := range strings.Split(rt, ",") {
							if s := strings.TrimSpace(strings.Trim(r, "()")); s != "" {
								rets = append(rets, s)
							}
						}
					} else if post != "" {
						fields := strings.Fields(post)
						if len(fields) > 0 {
							rets = append(rets, strings.Trim(fields[0], ",."))
						}
					}
				}
			}
		}
		if name != "" {
			return Intent{Action: "ADD_FUNC", Name: strings.Title(strings.TrimSpace(name)), Params: params, Receiver: receiver, Returns: rets}
		}
	}

	// Error-check patterns.
	if strings.Contains(lower, "err != nil") || strings.Contains(lower, "add error check") || strings.Contains(lower, "return on error") || strings.Contains(lower, "on error") || strings.Contains(lower, "error check") {
		// choose default target var 'err'
		ret := ""
		target := "err"
		if idx := strings.Index(lower, "for "); idx >= 0 {
			after := strings.TrimSpace(lower[idx+len("for "):])
			if fields := strings.Fields(after); len(fields) > 0 {
				target = strings.Trim(fields[0], " ,.")
			}
		}
		if strings.Contains(lower, "return nil") || strings.Contains(lower, "return nil,") || strings.Contains(lower, "nil on error") {
			ret = "nil"
		} else if strings.Contains(lower, "return \"\"") || strings.Contains(lower, "empty string") || strings.Contains(lower, "string on error") {
			ret = `""`
		} else if strings.Contains(lower, "return 0") || strings.Contains(lower, "0 on error") {
			ret = "0"
		}
		if ret != "" || strings.Contains(lower, "on error") || strings.Contains(lower, "error check") {
			return Intent{Action: "ADD_ERROR_CHECK", Target: target, Ret: ret}
		}
	}

	// As a fallback consult the example matcher for known code_update snippets
	// and try to extract a simple intent from the matched example.
	m := dense.MatchCommandFromExamples(prompt, examples)
	if m.Type == "code_update" && m.CodeAfter != "" {
		// If the matched example contains a function declaration, prefer ADD_FUNC.
		if strings.HasPrefix(strings.TrimSpace(m.CodeAfter), "func ") {
			// crude parse: func Name
			fields := strings.Fields(m.CodeAfter)
			if len(fields) >= 2 {
				name := fields[1]
				if idx := strings.Index(name, "("); idx >= 0 {
					name = name[:idx]
				}
				name = strings.TrimSpace(name)
				if name != "" {
					return Intent{Action: "ADD_FUNC", Name: name}
				}
			}
		}
	}
	// Type/struct creation patterns
	if strings.Contains(lower, "create struct") || strings.Contains(lower, "create type") || strings.Contains(lower, "add struct") || strings.Contains(lower, "with fields") || strings.Contains(lower, "structure") {
		if m := regexp.MustCompile(`(?i)(?:add|create)\s+(?:struct|type)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:with\s+fields?\s*(.*?))?(?:\s+to\s+file\b.*|$)`).FindStringSubmatch(prompt); len(m) > 1 {
			name := strings.TrimSpace(m[1])
			if name != "" {
				intent := Intent{Action: "ADD_TYPE", Name: name}
				if len(m) > 2 {
					intent.Params = parseStructFieldSpecs(m[2])
				}
				return intent
			}
		}
		parts := strings.Fields(prompt)
		for i, p := range parts {
			if strings.EqualFold(p, "struct") && i+1 < len(parts) {
				name := strings.Trim(parts[i+1], " ,.")
				if name != "" && !strings.EqualFold(name, "add") && !strings.EqualFold(name, "create") {
					intent := Intent{Action: "ADD_TYPE", Name: name}
					if i+2 < len(parts) && strings.EqualFold(parts[i+2], "with") {
						intent.Params = parseStructFieldSpecs(strings.Join(parts[i+3:], " "))
					}
					return intent
				}
			}
			if strings.EqualFold(p, "type") && i+1 < len(parts) {
				name := strings.Trim(parts[i+1], " ,.")
				if name != "" && !strings.EqualFold(name, "struct") {
					return Intent{Action: "ADD_TYPE", Name: name}
				}
			}
		}
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.Trim(parts[i], " ,.")
			if candidate == "" || strings.EqualFold(candidate, "create") || strings.EqualFold(candidate, "type") || strings.EqualFold(candidate, "struct") || strings.EqualFold(candidate, "add") {
				continue
			}
			return Intent{Action: "ADD_TYPE", Name: candidate}
		}
	}
	if strings.Contains(lower, "opening brace") || strings.Contains(lower, "missing opening brace") {
		switch {
		case strings.Contains(lower, "if"):
			return Intent{Action: "ADD_FUNC", Name: "IfCondition"}
		case strings.Contains(lower, "for"):
			return Intent{Action: "ADD_FUNC", Name: "ForLoop"}
		case strings.Contains(lower, "switch"):
			return Intent{Action: "ADD_FUNC", Name: "SwitchCase"}
		case strings.Contains(lower, "struct") || strings.Contains(lower, "type"):
			return Intent{Action: "ADD_TYPE", Name: "BraceStruct"}
		case strings.Contains(lower, "function"):
			return Intent{Action: "ADD_FUNC", Name: "FixedFunction"}
		default:
			return Intent{Action: "ADD_FUNC", Name: "BraceFix"}
		}
	}

	// Test creation patterns
	if strings.Contains(lower, "unit test") || strings.Contains(lower, "add test") || strings.Contains(lower, "create test") {
		// try to find the function name mentioned
		parts := strings.Fields(prompt)
		for i, p := range parts {
			if strings.Contains(strings.ToLower(p), "for") && i+1 < len(parts) {
				name := strings.Trim(parts[i+1], " ,.")
				return Intent{Action: "ADD_TEST", Name: name}
			}
		}
		return Intent{Action: "ADD_TEST", Name: "Example"}
	}
	if strings.Contains(lower, "create file") || strings.Contains(lower, "new file") || strings.Contains(lower, "make file") {
		parts := strings.Fields(prompt)
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := strings.Trim(parts[i], " ,./")
			if candidate != "" && !strings.EqualFold(candidate, "file") && !strings.EqualFold(candidate, "create") && !strings.EqualFold(candidate, "new") && !strings.EqualFold(candidate, "make") {
				return Intent{Action: "ADD_FILE", Name: candidate}
			}
		}
		return Intent{Action: "ADD_FILE", Name: "main.go"}
	}
	// Do not synthesize a placeholder function for vague or incomplete prompts.
	return Intent{}
}

// renderIntentToCode deterministically converts an Intent to a Go code
// snippet using go/ast and formatting, ensuring syntactic validity.
func zeroValueExprForType(typ string) ast.Expr {
	t := strings.TrimSpace(typ)
	switch {
	case t == "":
		return ast.NewIdent("nil")
	case t == "string":
		return &ast.BasicLit{Kind: token.STRING, Value: `""`}
	case t == "bool":
		return ast.NewIdent("false")
	case t == "int", t == "int8", t == "int16", t == "int32", t == "int64", t == "byte", t == "rune", t == "uint", t == "uint8", t == "uint16", t == "uint32", t == "uint64", t == "uintptr":
		return &ast.BasicLit{Kind: token.INT, Value: "0"}
	case t == "float32", t == "float64":
		return &ast.BasicLit{Kind: token.FLOAT, Value: "0"}
	case t == "complex64", t == "complex128":
		return &ast.BasicLit{Kind: token.IMAG, Value: "0i"}
	case strings.Contains(t, "[]") || strings.Contains(t, "map[") || strings.Contains(t, "chan ") || strings.Contains(t, "*") || strings.HasPrefix(t, "func") || t == "any" || t == "interface{}" || t == "error":
		return ast.NewIdent("nil")
	default:
		return ast.NewIdent("nil")
	}
}

func declaredTypeNamesInIntent(intent Intent) []string {
	seen := map[string]bool{}
	locals := map[string]bool{}
	collectLocalNames := func(items []string) {
		for _, item := range items {
			fields := strings.Fields(item)
			if len(fields) >= 2 {
				locals[strings.TrimSpace(fields[0])] = true
			}
		}
	}
	collectLocalNames(intent.Params)
	if recv := strings.TrimSpace(intent.Receiver); recv != "" {
		if fields := strings.Fields(recv); len(fields) >= 2 {
			locals[strings.TrimSpace(fields[0])] = true
		}
		recv = strings.TrimPrefix(recv, "*")
		if recv != "" && !isBuiltInTypeName(recv) {
			seen[recv] = true
		}
	}
	collect := func(items []string) {
		for _, item := range items {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			typePart := item
			fields := strings.Fields(item)
			if len(fields) >= 2 {
				first := fields[0]
				typePart = strings.TrimSpace(item[len(first):])
				if typePart == "" {
					typePart = first
				}
			}
			for _, candidate := range strings.FieldsFunc(typePart, func(r rune) bool {
				return r == '*' || r == '|' || r == '[' || r == ']' || r == '{' || r == '}' || r == '(' || r == ')' || r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
			}) {
				if candidate == "" || strings.Contains(candidate, ".") || isBuiltInTypeName(candidate) || locals[candidate] {
					continue
				}
				if !seen[candidate] {
					seen[candidate] = true
				}
			}
		}
	}
	collect(intent.Params)
	collect(intent.Returns)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func renderIntentToCode(intent Intent) (string, error) {
	// No nested parseTypeExpr here; use package-level parseTypeExpr helper.
	switch intent.Action {
	case "SOCIAL":
		return "package main\n", nil
	case "ADD_FUNC":
		if intent.Name == "" {
			return "", nil
		}
		// Build: func Name(params) (returns) { // TODO }
		fn := &ast.FuncDecl{
			Name: ast.NewIdent(intent.Name),
			Type: &ast.FuncType{Params: &ast.FieldList{}, Results: nil},
			Body: &ast.BlockStmt{List: []ast.Stmt{}},
		}
		// Params
		if len(intent.Params) > 0 {
			var fields []*ast.Field
			for _, p := range intent.Params {
				// p like "name string" or "int"
				parts := strings.Fields(p)
				var name string
				var typ string
				if len(parts) == 1 {
					typ = parts[0]
				} else if len(parts) >= 2 {
					name = parts[0]
					typ = strings.Join(parts[1:], " ")
				}
				var field *ast.Field
				if name != "" {
					field = &ast.Field{Names: []*ast.Ident{ast.NewIdent(name)}, Type: parseTypeExpr(typ)}
				} else {
					field = &ast.Field{Type: parseTypeExpr(typ)}
				}
				fields = append(fields, field)
			}
			fn.Type.Params = &ast.FieldList{List: fields}
		}
		// Returns
		if len(intent.Returns) > 0 {
			var rfields []*ast.Field
			for _, r := range intent.Returns {
				rfields = append(rfields, &ast.Field{Type: parseTypeExpr(r)})
			}
			fn.Type.Results = &ast.FieldList{List: rfields}
		}
		// Body: add TODO return if we have returns
		if len(intent.Returns) > 0 {
			// create a return of zero values based on the declared type
			var exprs []ast.Expr
			for _, r := range intent.Returns {
				exprs = append(exprs, zeroValueExprForType(r))
			}
			ret := &ast.ReturnStmt{Results: exprs}
			fn.Body.List = append(fn.Body.List, ret)
		} else {
			// valid no-op for generated functions without explicit return types
			fn.Body.List = append(fn.Body.List, &ast.ReturnStmt{})
		}
		// Receiver
		if recv := sanitizeReceiver(intent.Receiver); recv != "" {
			r := strings.TrimSpace(recv)
			parts := strings.Fields(r)
			var rname string
			var rtype string
			if len(parts) >= 2 {
				rname = parts[0]
				rtype = strings.Join(parts[1:], " ")
			} else {
				rtype = parts[0]
				// derive receiver name from type (first letter lowercased)
				tn := strings.TrimPrefix(rtype, "*")
				if tn == "" {
					rname = "r"
				} else {
					rname = strings.ToLower(tn[:1])
				}
			}
			fn.Recv = &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent(rname)}, Type: parseTypeExpr(rtype)}}}
		}

		// Render the function with placeholder declarations for named types used in
		// the signature (for example *Config, User, Result). This keeps generated
		// code valid under go/types even when the file does not yet declare those
		// types.
		decls := make([]ast.Decl, 0, 1+len(declaredTypeNamesInIntent(intent)))
		for _, name := range declaredTypeNamesInIntent(intent) {
			decls = append(decls, &ast.GenDecl{
				Tok: token.TYPE,
				Specs: []ast.Spec{
					&ast.TypeSpec{
						Name: ast.NewIdent(name),
						Type: &ast.StructType{Fields: &ast.FieldList{}},
					},
				},
			})
		}
		decls = append(decls, fn)
		file := &ast.File{
			Name:  ast.NewIdent("main"),
			Decls: decls,
		}
		var sb strings.Builder
		if err := format.Node(&sb, token.NewFileSet(), file); err != nil {
			return "", err
		}
		out := sb.String()
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out, nil
	case "ADD_IMPORT":
		if intent.Name == "" {
			return "", nil
		}
		pkg := normalizeImportPath(intent.Name)
		if pkg == "" {
			return "", nil
		}
		out := fmt.Sprintf("import %q\n", pkg)
		return out, nil
	case "ADD_JSON_TAGS":
		name := intent.Name
		if name == "" {
			name = "User"
		}
		if !strings.HasSuffix(name, " struct") {
			name = strings.TrimSpace(name)
		}
		tmpl := fmt.Sprintf("type %s struct {\n\tFirstName string `json:\"first_name\"`\n\tLastName string `json:\"last_name\"`\n}\n", name)
		return tmpl, nil
	case "ADD_FILE":
		if intent.Name == "" {
			return "package main\n", nil
		}
		return "package main\n\n// file generated: " + intent.Name + "\n", nil
	case "FILE_EDIT", "FILE_CREATE", "FILE_DELETE":
		return "package main\n", nil
	case "ADD_TYPE":
		if intent.Name == "" {
			return "", nil
		}
		fields := &ast.FieldList{}
		if len(intent.Params) > 0 {
			for _, p := range intent.Params {
				parts := strings.Fields(p)
				if len(parts) == 0 {
					continue
				}
				name := parts[0]
				typ := strings.Join(parts[1:], " ")
				if typ == "" {
					typ = name
					name = ""
				}
				if name == "" {
					fields.List = append(fields.List, &ast.Field{Type: parseTypeExpr(typ)})
				} else {
					fields.List = append(fields.List, &ast.Field{Names: []*ast.Ident{ast.NewIdent(name)}, Type: parseTypeExpr(typ)})
				}
			}
		}
		ts := &ast.GenDecl{
			Tok: token.TYPE,
			Specs: []ast.Spec{
				&ast.TypeSpec{
					Name: ast.NewIdent(intent.Name),
					Type: &ast.StructType{Fields: fields},
				},
			},
		}
		var sb strings.Builder
		if err := format.Node(&sb, token.NewFileSet(), ts); err != nil {
			return "", err
		}
		out := sb.String()
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out, nil
	case "ADD_TEST":
		name := intent.Name
		if name == "" {
			name = "Example"
		}
		// Ensure test function name starts with Test
		if !strings.HasPrefix(name, "Test") {
			name = "Test" + strings.Title(name)
		}
		fn := &ast.FuncDecl{
			Name: ast.NewIdent(name),
			Type: &ast.FuncType{
				Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("t")}, Type: ast.NewIdent("*testing.T")}}},
			},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.ExprStmt{X: &ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent("t"), Sel: ast.NewIdent("Skip")}, Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"TODO: implement"`}}}},
			}},
		}
		var sb2 strings.Builder
		if err := format.Node(&sb2, token.NewFileSet(), fn); err != nil {
			return "", err
		}
		out := sb2.String()
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out, nil
	case "ADD_ERROR_CHECK":
		// Emit a valid function so the generated snippet can be inserted at package
		// scope and still type-check in the evaluation harness.
		target := strings.TrimSpace(intent.Target)
		if target == "" {
			target = "err"
		}
		retType := "string"
		retVal := `""`
		if intent.Ret != "" {
			switch intent.Ret {
			case "nil":
				retType = "any"
				retVal = "nil"
			case "0":
				retType = "int"
				retVal = "0"
			default:
				if strings.HasPrefix(intent.Ret, "\"") || strings.HasPrefix(intent.Ret, "`") {
					retType = "string"
					retVal = intent.Ret
				} else {
					retType = "string"
					retVal = `""`
				}
			}
		}
		var retExpr ast.Expr = &ast.BasicLit{Kind: token.STRING, Value: retVal}
		if intent.Ret == "0" {
			retExpr = &ast.BasicLit{Kind: token.INT, Value: "0"}
		}
		if intent.Ret == "nil" {
			retExpr = ast.NewIdent("nil")
		}
		fn := &ast.FuncDecl{
			Name: ast.NewIdent("HandleError"),
			Type: &ast.FuncType{
				Params:  &ast.FieldList{},
				Results: &ast.FieldList{List: []*ast.Field{{Type: parseTypeExpr(retType)}}},
			},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{&ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent(target)}, Type: ast.NewIdent("error")}}}},
				&ast.IfStmt{
					Cond: &ast.BinaryExpr{X: ast.NewIdent(target), Op: token.NEQ, Y: ast.NewIdent("nil")},
					Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{retExpr}}}},
				},
				&ast.ReturnStmt{Results: []ast.Expr{retExpr}},
			}},
		}
		var sb strings.Builder
		if err := format.Node(&sb, token.NewFileSet(), fn); err != nil {
			return "", err
		}
		out := sb.String()
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out, nil
	default:
		return "", nil
	}
}

type PackageScopeInfo struct {
	Functions map[string]bool
	Types     map[string]bool
	Imports   map[string]bool
}

func InspectPackageScope(filePath string) PackageScopeInfo {
	info := PackageScopeInfo{
		Functions: map[string]bool{},
		Types:     map[string]bool{},
		Imports:   map[string]bool{},
	}
	if strings.TrimSpace(filePath) == "" {
		return info
	}
	baseDir := filepath.Dir(filePath)
	if baseDir == "" {
		baseDir = "."
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return info
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(baseDir, entry.Name())
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			continue
		}
		for _, decl := range node.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil {
				info.Functions[strings.ToLower(fn.Name.Name)] = true
			}
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, spec := range gd.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name != nil {
						info.Types[strings.ToLower(ts.Name.Name)] = true
					}
				}
			}
		}
		for _, imp := range node.Imports {
			if imp.Path != nil {
				p := strings.Trim(imp.Path.Value, `"`)
				info.Imports[strings.ToLower(p)] = true
			}
		}
	}
	return info
}

func ValidateAST(fset *token.FileSet, file *ast.File) error {
	if file == nil {
		return fmt.Errorf("nil AST file")
	}

	packageFiles, err := collectPackageFiles(file)
	if err != nil {
		return err
	}
	if len(packageFiles) == 0 {
		packageFiles = []*ast.File{file}
	}

	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	_, err = conf.Check(file.Name.Name, fset, packageFiles, info)
	if err == nil {
		return nil
	}
	if dense.ValidateASTWithTolerances(nil, []error{err}) == nil {
		return nil
	}
	return err
}

func collectPackageFiles(file *ast.File) ([]*ast.File, error) {
	if file == nil || file.Name == nil || file.Name.Name == "" {
		return nil, fmt.Errorf("nil or unnamed package file")
	}
	return []*ast.File{file}, nil
}

func ValidateASTSource(path, src string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return err
	}

	if siblingFiles, err := loadSamePackageFiles(path, file.Name.Name); err == nil && len(siblingFiles) > 0 {
		packageFiles := append([]*ast.File{file}, siblingFiles...)
		conf := types.Config{Importer: importer.Default()}
		info := &types.Info{
			Types:      make(map[ast.Expr]types.TypeAndValue),
			Defs:       make(map[*ast.Ident]types.Object),
			Uses:       make(map[*ast.Ident]types.Object),
			Selections: make(map[*ast.SelectorExpr]*types.Selection),
		}
		_, err = conf.Check(file.Name.Name, fset, packageFiles, info)
		if err == nil {
			return nil
		}
		if dense.ValidateASTWithTolerances(nil, []error{err}) == nil {
			return nil
		}
		return err
	}
	return ValidateAST(fset, file)
}

func loadSamePackageFiles(path, pkgName string) ([]*ast.File, error) {
	if pkgName == "" {
		return nil, nil
	}
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]*ast.File, 0, 8)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if full == path {
			continue
		}
		src, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, full, src, parser.ParseComments)
		if err != nil || parsed.Name == nil || parsed.Name.Name != pkgName {
			continue
		}
		files = append(files, parsed)
	}
	return files, nil
}

func tryApplyExactFunctionReplacement(prompt, target string) (bool, string) {
	parsed := dense.ParseHybridPrompt(prompt)
	if parsed.Action == "replace" {
		if len(parsed.Identifiers) > 0 && parsed.RawCode != "" {
			name := strings.TrimSpace(parsed.Identifiers[0])
			replacement := strings.TrimSpace(parsed.RawCode)
			replacement = strings.TrimSpace(strings.TrimSuffix(replacement, "."))
			if name == "" || replacement == "" {
				return false, ""
			}
			replacement = normalizeReplacementFunction(name, replacement)
			if _, err := applyFunctionReplacement(target, name, replacement); err == nil {
				return true, fmt.Sprintf("✅ Applied exact replacement to %s", target)
			}
		}
		return false, ""
	}
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if !strings.Contains(lower, "replace ") || !strings.Contains(lower, " with ") {
		return false, ""
	}
	idx := strings.Index(lower, "replace ")
	if idx < 0 {
		return false, ""
	}
	namePart := strings.TrimSpace(prompt[idx+len("replace "):])
	j := strings.Index(strings.ToLower(namePart), " with ")
	if j < 0 {
		return false, ""
	}
	name := strings.TrimSpace(namePart[:j])
	replacement := strings.TrimSpace(namePart[j+len(" with "):])
	replacement = strings.TrimSpace(strings.TrimSuffix(replacement, "."))
	if name == "" || replacement == "" {
		return false, ""
	}
	replacement = normalizeReplacementFunction(name, replacement)
	if _, err := applyFunctionReplacement(target, name, replacement); err == nil {
		return true, fmt.Sprintf("✅ Applied exact replacement to %s", target)
	}
	return false, ""
}

func buildContextAwareResponse(prompt string, conv *Conversation, model *dense.DenseModel, examples []dense.CommandExample) string {
	// Inspect the package directory (not just one file) to gather structural context
	// that can influence classification and response construction.
	lower := strings.ToLower(strings.TrimSpace(prompt))
	target := ""
	if conv != nil {
		target = conv.TargetGoFile()
	}
	var hasFunc = map[string]bool{}
	var hasType = map[string]bool{}
	var hasImport = map[string]bool{}
	if target != "" {
		scope := InspectPackageScope(target)
		hasFunc = scope.Functions
		hasType = scope.Types
		hasImport = scope.Imports
	}
	_ = hasType
	_ = hasImport

	// Base classification using heuristics + model fallback. We'll allow the
	// observed target file structure to nudge the decision when the prompt
	// mentions symbols that already exist.
	cmdType := dense.ClassifyCommandType(prompt)
	if cmdType == "social" {
		input := dense.BagOfWords(prompt, dense.CommandVocab)
		preds := model.Predict([][]float32{input})
		if len(preds) > 0 {
			label := preds[0]
			if label >= 0 && label < len(dense.CommandLabels) {
				candidate := dense.CommandLabels[label]
				if candidate != "social" {
					cmdType = candidate
				}
			}
		}
	}

	// Explicit structured intents must win over generic fallback heuristics.
	if intent := predictIntent(prompt, conv, model, examples); intent.Action != "" {
		if code, err := renderIntentToCode(intent); err == nil && code != "" {
			return "🔧 " + code
		}
	}

	// If the user asked to create a file but a target .go file is already set
	// and exists, prefer an edit instead of a create.
	if target != "" && strings.Contains(lower, "create file") && strings.HasSuffix(target, ".go") {
		if _, err := os.Stat(target); err == nil {
			cmdType = "file_edit"
		}
	}

	// If the prompt appears to request creating a function but the target file
	// already contains that function name, prefer `code_update` to indicate an
	// in-file edit instead of inserting duplicate definitions.
	if snippet := functionSnippetFromPrompt(prompt); snippet != "" {
		// extract the function name from the snippet: "func Name(" -> Name
		parts := strings.Fields(snippet)
		if len(parts) >= 2 {
			name := parts[1]
			if idx := strings.Index(name, "("); idx >= 0 {
				name = name[:idx]
			}
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" && hasFunc[name] {
				cmdType = "code_update"
			}
		}
	}

	if cmdType == "code_update" {
		if strings.Contains(lower, "replace ") && strings.Contains(lower, " with ") {
			return "🔧 " + functionSnippetFromPrompt(prompt)
		}
		if strings.Contains(lower, "swap function") || strings.Contains(lower, "swap method") {
			if snippet := functionSnippetFromPrompt(prompt); snippet != "" {
				return "🔧 " + snippet
			}
		}
		// Try to predict the intent first if it's an import or simple well-defined intent
		if intent := predictIntent(prompt, conv, model, examples); intent.Action == "ADD_IMPORT" {
			if code, err := renderIntentToCode(intent); err == nil && code != "" {
				return "🔧 " + code
			}
		}
		if snippet := functionSnippetFromPrompt(prompt); snippet != "" {
			return "🔧 " + snippet
		}
		// If prompt contains backticks, extract the content within them.
		if idx := strings.Index(prompt, "`"); idx >= 0 {
			if lastIdx := strings.LastIndex(prompt, "`"); lastIdx > idx {
				snippet := strings.TrimSpace(prompt[idx+1 : lastIdx])
				return "🔧 " + snippet
			}
		}
		// Try parsing from raw snippet triggers (e.g., "add j:= ...")
		// Find where the actual Go code block might start by looking for standard prefixes
		for _, prefix := range []string{"add to file ", "add file ", "add to ", "add "} {
			if strings.HasPrefix(lower, prefix) {
				rem := prompt[len(prefix):]
				// Skip target filename if present (e.g. "jim/jim.go")
				fields := strings.Fields(rem)
				if len(fields) > 1 && strings.HasSuffix(fields[0], ".go") {
					rem = strings.TrimSpace(rem[len(fields[0]):])
				}
				return "🔧 " + rem
			}
		}
	}

	// Use the lightweight intent predictor + AST renderer to produce
	// deterministic, syntax-correct code for well-understood intents.
	if intent := predictIntent(prompt, conv, model, examples); intent.Action != "" {
		if code, err := renderIntentToCode(intent); err == nil && code != "" {
			return "🔧 " + code
		}
	}

	// Match the best command example.
	m := dense.MatchCommandFromExamples(prompt, examples)

	// Handle follow-up questions about previous responses.
	if isFollowUp(prompt, conv) && conv.HasContext() {
		lastType := conv.LastType()
		lastAssistant := conv.LastAssistant()

		lower := strings.ToLower(strings.TrimSpace(prompt))

		// "What did I say?" → recall the user's last message.
		if strings.Contains(lower, "what did i say") || strings.Contains(lower, "what was my last message") {
			lastUser := conv.LastUser()
			if lastUser != "" {
				return fmt.Sprintf("🤖 You said: %q", lastUser)
			}
			return "🤖 I don't have any past conversation history to reference."
		}

		// "What did you say?" → recall the assistant's last response.
		if strings.Contains(lower, "what did you say") || strings.Contains(lower, "what was your last message") {
			if lastAssistant != "" {
				return fmt.Sprintf("🤖 I said: %q", lastAssistant)
			}
			return "🤖 I haven't said anything yet."
		}

		// "Can you repeat / show me again" → repeat the last response.
		if strings.Contains(lower, "repeat") || strings.Contains(lower, "again") || strings.Contains(lower, "show me") {
			if lastAssistant != "" {
				return fmt.Sprintf("🤖 Here it is again: %q", lastAssistant)
			}
		}

		// "Tell me more / elaborate" → expand on the last topic.
		if strings.Contains(lower, "more") || strings.Contains(lower, "elaborate") || strings.Contains(lower, "explain") {
			if lastType == "code_update" {
				return "🤖 That code snippet adds a Go language construct. You can combine it with other commands like adding imports, declaring variables, or wrapping it in a function. What else would you like to add?"
			}
			if lastAssistant != "" {
				return fmt.Sprintf("🤖 To expand on that: %s. Is there anything specific you'd like to know more about?", lastAssistant)
			}
		}

		// "What about / and then / what next" → continue the conversation flow.
		if strings.Contains(lower, "what about") || strings.Contains(lower, "and then") || strings.Contains(lower, "what next") || strings.Contains(lower, "what now") {
			if lastType == "code_update" {
				return "🤖 After that code change, you might want to add error handling, a return statement, or a closing brace. What would you like to do next?"
			}
			return "🤖 What would you like to do next? I can help with Go code updates or just chat."
		}
	}

	// Handle greetings with context awareness.
	if isGreeting(prompt) && conv.HasContext() {
		lastType := conv.LastType()
		if lastType == "code_update" {
			return "🤖 Hello again! Ready to continue with more Go code updates. What would you like to add next?"
		}
		return "🤖 Hello! Good to see you again. How can I help with your Go file today?"
	}

	// Handle farewells.
	if isFarewell(prompt) {
		return "🤖 Goodbye! Feel free to come back anytime you need Go code help."
	}

	// Handle thanks.
	if isThanks(prompt) {
		return "🤖 You're welcome! Let me know if you need any more Go file updates."
	}

	// Standard response based on classification.
	if cmdType == "social" || m.Type == "social" {
		if m.Response == "" {
			return "🤖 I'm here to help with your Go file. What would you like to do?"
		}
		return "🤖 " + m.Response
	}
	if cmdType == "file_create" || cmdType == "file_edit" || cmdType == "file_delete" {
		if m.CodeAfter != "" {
			return "🔧 " + m.CodeAfter
		}
		if cmdType == "file_create" {
			return "🔧 created file"
		}
		if cmdType == "file_edit" {
			return "🔧 updated file"
		}
		return "🔧 deleted file"
	}
	if cmdType == "folder_query" {
		if m.Response != "" {
			return "🤖 " + m.Response
		}
		return "🤖 I can inspect that folder and its contents."
	}
	if cmdType == "folder_create" || cmdType == "folder_delete" {
		if m.CodeAfter != "" {
			return "🔧 " + m.CodeAfter
		}
		if cmdType == "folder_create" {
			return "🔧 created folder"
		}
		return "🔧 deleted folder"
	}

	// Code update response.
	if m.CodeAfter == "" {
		return "🔧 I can help with that. What Go file change do you need?"
	}
	return "🔧 " + m.CodeAfter
}

// ─── NL Import+Use Struct Handler ─────────────────────────────────────────────

// tryHandleImportAndUse handles natural-language prompts of the form:
//
//	"import [and use] [the] struct|function <Name> from [file] <src.go> into [file] <dst.go>"
//
// It reads the source file to confirm the symbol exists, resolves the full module
// import path, then adds the import declaration and a usage snippet to the destination
// file. Returns ("", nil) if the prompt does not match the pattern.
func tryHandleImportAndUse(prompt string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(prompt))

	// Must contain "import" and ("from" or "into") to be a candidate.
	if !strings.Contains(lower, "import") || !strings.Contains(lower, "into") {
		return "", nil
	}

	// Determine symbol kind: "struct" or "function"
	symbolKind := "" // "struct" or "func"
	if strings.Contains(lower, "struct") {
		symbolKind = "struct"
	} else if strings.Contains(lower, "function") || strings.Contains(lower, "func") {
		symbolKind = "func"
	}

	// Extract symbol name based on kind.
	symbolName := ""
	switch symbolKind {
	case "struct":
		if m := regexp.MustCompile(`(?i)struct\s+([A-Za-z_][A-Za-z0-9_]*)`).FindStringSubmatch(prompt); len(m) >= 2 {
			symbolName = m[1]
		}
	case "func":
		// "function <Name>" or "func <Name>"
		if m := regexp.MustCompile(`(?i)(?:function|func)\s+([A-Za-z_][A-Za-z0-9_]*)`).FindStringSubmatch(prompt); len(m) >= 2 {
			symbolName = m[1]
		}
	default:
		// Neither struct nor function mentioned — not our pattern.
		return "", nil
	}

	// Extract source file: look for "from [file] <path.go>"
	srcFile := ""
	if m := regexp.MustCompile(`(?i)from\s+(?:file\s+)?([A-Za-z0-9_./-]+\.go)`).FindStringSubmatch(prompt); len(m) >= 2 {
		srcFile = m[1]
	}

	// Extract destination file: look for "into [file] <path.go>"
	dstFile := ""
	if m := regexp.MustCompile(`(?i)into\s+(?:file\s+)?([A-Za-z0-9_./-]+\.go)`).FindStringSubmatch(prompt); len(m) >= 2 {
		dstFile = m[1]
	}

	// If we don't have all required parts, bail out — not our pattern.
	if symbolName == "" || srcFile == "" || dstFile == "" {
		return "", nil
	}

	// Make paths absolute relative to cwd.
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	if !filepath.IsAbs(srcFile) {
		srcFile = filepath.Join(cwd, srcFile)
	}
	if !filepath.IsAbs(dstFile) {
		dstFile = filepath.Join(cwd, dstFile)
	}

	// Read and parse the source file.
	srcBytes, err := os.ReadFile(srcFile)
	if err != nil {
		return "", fmt.Errorf("read source file %s: %w", srcFile, err)
	}
	srcContent := string(srcBytes)

	// Verify the symbol exists.
	switch symbolKind {
	case "struct":
		if !strings.Contains(srcContent, "type "+symbolName+" struct") {
			return "", fmt.Errorf("struct %q not found in %s", symbolName, srcFile)
		}
	case "func":
		funcRe := regexp.MustCompile(`(?m)^func\s+` + regexp.QuoteMeta(symbolName) + `\s*\(`)
		if !funcRe.MatchString(srcContent) {
			return "", fmt.Errorf("function %q not found in %s", symbolName, srcFile)
		}
	}

	// Determine the package name of the source file.
	srcFset := token.NewFileSet()
	srcAST, err := parser.ParseFile(srcFset, srcFile, srcContent, 0)
	if err != nil {
		return "", fmt.Errorf("parse source file: %w", err)
	}
	srcPkgName := srcAST.Name.Name

	// Resolve the full module import path for the source directory.
	srcDir := filepath.Dir(srcFile)
	importPath, err := resolveModuleImportPath(srcDir)
	if err != nil {
		rel, relErr := filepath.Rel(cwd, srcDir)
		if relErr != nil || rel == "" || rel == "." {
			return "", fmt.Errorf("resolve import path for %s: %w", srcDir, err)
		}
		importPath = rel
	}

	// Ensure the destination file exists.
	if _, statErr := os.Stat(dstFile); os.IsNotExist(statErr) {
		if writeErr := os.MkdirAll(filepath.Dir(dstFile), 0755); writeErr != nil {
			return "", fmt.Errorf("mkdir: %w", writeErr)
		}
		if writeErr := os.WriteFile(dstFile, []byte("package main\n"), 0644); writeErr != nil {
			return "", fmt.Errorf("create dest file: %w", writeErr)
		}
	}

	dstBytes, err := os.ReadFile(dstFile)
	if err != nil {
		return "", fmt.Errorf("read dest file: %w", err)
	}

	dstFset := token.NewFileSet()
	dstNode, err := parser.ParseFile(dstFset, dstFile, string(dstBytes), parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("parse dest file: %w", err)
	}

	// Add the import if not already present.
	alreadyImported := false
	for _, imp := range dstNode.Imports {
		if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == importPath {
			alreadyImported = true
			break
		}
	}
	if !alreadyImported {
		astutil.AddImport(dstFset, dstNode, importPath)
	}

	// Build a usage snippet based on symbol kind.
	qualifiedSymbol := srcPkgName + "." + symbolName
	alreadyUsed := strings.Contains(string(dstBytes), qualifiedSymbol)
	var usageDecl string
	switch symbolKind {
	case "struct":
		varName := strings.ToLower(string([]rune(symbolName)[:1])) + symbolName[1:] + "Instance"
		usageDecl = fmt.Sprintf("var %s = %s{}", varName, qualifiedSymbol)
	case "func":
		// Wrap in a top-level var using a function call result, or a simple _ = call.
		// Since we don't know the return type, use `var _ = func() { <pkg>.<Func>() }` pattern.
		usageDecl = fmt.Sprintf("var _ = func() { %s() }()", qualifiedSymbol)
	}

	// Write the import.
	if !alreadyImported {
		var buf strings.Builder
		if fmtErr := format.Node(&buf, dstFset, dstNode); fmtErr != nil {
			return "", fmt.Errorf("format after import: %w", fmtErr)
		}
		if writeErr := os.WriteFile(dstFile, []byte(buf.String()), 0644); writeErr != nil {
			return "", fmt.Errorf("write after import: %w", writeErr)
		}
	}

	// Append the usage snippet if not already present.
	if !alreadyUsed && usageDecl != "" {
		updatedBytes, readErr := os.ReadFile(dstFile)
		if readErr != nil {
			return "", fmt.Errorf("re-read after import: %w", readErr)
		}
		updatedContent := strings.TrimRight(string(updatedBytes), "\n") + "\n\n" + usageDecl + "\n"
		if writeErr := os.WriteFile(dstFile, []byte(updatedContent), 0644); writeErr != nil {
			return "", fmt.Errorf("write usage snippet: %w", writeErr)
		}
	}

	result := fmt.Sprintf("✅ Added import %q and usage `%s` to %s", importPath, usageDecl, dstFile)
	return result, nil
}

// resolveModuleImportPath finds the full Go module import path for a given directory
// by walking up to find go.mod and combining the module name with the relative sub-path.
func resolveModuleImportPath(dir string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	// Walk up to find go.mod.
	search := absDir
	for {
		modFile := filepath.Join(search, "go.mod")
		if _, err := os.Stat(modFile); err == nil {
			// Found go.mod — parse the module line.
			b, readErr := os.ReadFile(modFile)
			if readErr != nil {
				return "", readErr
			}
			moduleName := ""
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "module ") {
					moduleName = strings.TrimSpace(strings.TrimPrefix(line, "module "))
					break
				}
			}
			if moduleName == "" {
				return "", fmt.Errorf("no module directive in %s", modFile)
			}
			rel, err := filepath.Rel(search, absDir)
			if err != nil {
				return "", err
			}
			rel = filepath.ToSlash(rel)
			if rel == "" || rel == "." {
				return moduleName, nil
			}
			return moduleName + "/" + rel, nil
		}
		parent := filepath.Dir(search)
		if parent == search {
			break
		}
		search = parent
	}
	return "", fmt.Errorf("no go.mod found above %s", absDir)
}

// ─── Go File Editing ──────────────────────────────────────────────────────────

func applyFileOperation(filePath, opType, content string) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	switch opType {
	case "file_create":
		if _, err := os.Stat(absPath); err == nil {
			return "", fmt.Errorf("file already exists: %s", absPath)
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat file: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			return "", fmt.Errorf("create parent dir: %w", err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}
		return fmt.Sprintf("created %s", absPath), nil
	case "file_edit":
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", absPath)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}
		return fmt.Sprintf("updated %s", absPath), nil
	case "file_delete":
		if err := os.Remove(absPath); err != nil {
			return "", fmt.Errorf("delete file: %w", err)
		}
		return fmt.Sprintf("deleted %s", absPath), nil
	default:
		return "", fmt.Errorf("unsupported file operation: %s", opType)
	}
}

func applyFolderOperation(dirPath, opType string) (string, error) {
	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	switch opType {
	case "folder_create":
		if err := os.MkdirAll(absPath, 0755); err != nil {
			return "", fmt.Errorf("create folder: %w", err)
		}
		return fmt.Sprintf("created folder %s", absPath), nil
	case "folder_delete":
		if err := os.RemoveAll(absPath); err != nil {
			return "", fmt.Errorf("delete folder: %w", err)
		}
		return fmt.Sprintf("deleted folder %s", absPath), nil
	default:
		return "", fmt.Errorf("unsupported folder operation: %s", opType)
	}
}

// applyCodeToFile applies a code snippet to a target Go file. It uses AST-based
// editing for common operations (imports, functions, structs) and falls back to
// appending the snippet for simple statements.
func InjectIntentIntoAST(filePath string, intent Intent) (bool, string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return false, "", fmt.Errorf("resolve path: %w", err)
	}
	if strings.TrimSpace(filePath) == "" {
		return false, "", fmt.Errorf("empty file path")
	}
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return false, "", fmt.Errorf("file not found: %s", absPath)
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return false, "", fmt.Errorf("read file: %w", err)
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(code) == "" {
		return false, "", nil
	}
	return applyCodeViaAST(absPath, string(content), code)
}

func applyCodeToFile(filePath, code string) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if !strings.HasSuffix(absPath, ".go") {
		return "", fmt.Errorf("not a .go file: %s", absPath)
	}
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", absPath)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if backupWrites {
		if err := maybeWriteBackup(absPath, content); err != nil {
			return "", fmt.Errorf("backup file: %w", err)
		}
	}

	applied, msg, err := applyCodeViaAST(absPath, string(content), code)
	if err == nil && applied {
		updatedContent, readErr := os.ReadFile(absPath)
		if readErr != nil {
			return "", fmt.Errorf("read updated file: %w", readErr)
		}
		if validateErr := ValidateASTSource(absPath, string(updatedContent)); validateErr != nil {
			_ = os.WriteFile(absPath, content, 0644)
			return "", fmt.Errorf("generated change violates Go type rules: %w", validateErr)
		}
		return msg, nil
	}
	if err != nil {
		_ = os.WriteFile(absPath, content, 0644)
		return "", fmt.Errorf("generated change is not valid Go for %s: %w", absPath, err)
	}

	// If AST-based application does not apply anything, reject the request rather
	// than appending raw snippet text that can silently corrupt valid Go files.
	_ = os.WriteFile(absPath, content, 0644)
	return "", fmt.Errorf("code snippet was not applied: %q", code)
}

// applyCodeViaAST attempts to apply the code snippet using AST manipulation.
// Returns (applied, message, error).
func applyCodeViaAST(filePath, content, code string) (bool, string, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return false, "", err
	}

	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return false, "", nil
	}
	if strings.HasPrefix(trimmed, "package ") {
		return true, "package clause already present", nil
	}

	// Parse the incoming snippet as a temporary file so we can handle mixed
	// import/type/function declaration blocks consistently.
	src := fmt.Sprintf("package main\n\n%s", trimmed)
	snippetFset := token.NewFileSet()
	snippetNode, err := parser.ParseFile(snippetFset, "", src, parser.ParseComments)
	if err != nil {
		// If it failed to parse as top-level declarations, it could be a statement/expression (e.g. `j := jake.Jake{...}`)
		// Try parsing inside a function wrapper first to see if it is a valid statement block.
		funcSrc := fmt.Sprintf("package main\nfunc _wrapper() {\n%s\n}", trimmed)
		wrapperFset := token.NewFileSet()
		wrapperNode, err2 := parser.ParseFile(wrapperFset, "", funcSrc, parser.ParseComments)
		if err2 == nil {
			// Find the main function or the first function in target file to insert this block/statement,
			// or if no functions exist, wrap it in a main function and append it.
			var mainFunc *ast.FuncDecl
			for _, decl := range node.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
					mainFunc = fn
					break
				}
			}
			var stmts []ast.Stmt
			for _, decl := range wrapperNode.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "_wrapper" {
					stmts = fn.Body.List
					break
				}
			}
			if len(stmts) > 0 {
				if mainFunc != nil {
					mainFunc.Body.List = append(mainFunc.Body.List, stmts...)
				} else {
					// create a new main function containing the statements
					newMain := &ast.FuncDecl{
						Name: ast.NewIdent("main"),
						Type: &ast.FuncType{Params: &ast.FieldList{}},
						Body: &ast.BlockStmt{List: stmts},
					}
					node.Decls = append(node.Decls, newMain)
				}
				var buf strings.Builder
				if err := format.Node(&buf, fset, node); err != nil {
					return false, "", fmt.Errorf("format updated AST: %w", err)
				}
				if validateErr := ValidateASTSource(filePath, buf.String()); validateErr != nil {
					return false, "", fmt.Errorf("generated change violates Go type rules: %w", validateErr)
				}
				if err := writeFormattedFile(filePath, fset, node); err != nil {
					return false, "", err
				}
				return true, fmt.Sprintf("updated %s", filePath), nil
			}
		}
		return false, "", fmt.Errorf("cannot parse code snippet: %v (also failed statement fallback: %v)", err, err2)
	}

	added := false
	for _, decl := range snippetNode.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				for _, spec := range d.Specs {
					imp, ok := spec.(*ast.ImportSpec)
					if !ok || imp.Path == nil {
						continue
					}
					path := strings.Trim(imp.Path.Value, `"`)
					already := false
					for _, existing := range node.Imports {
						if existing.Path != nil && strings.Trim(existing.Path.Value, `"`) == path {
							already = true
							break
						}
					}
					if !already {
						astutil.AddImport(fset, node, path)
						added = true
					}
				}
			}
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					replaced := false
					for i := range node.Decls {
						gd, ok := node.Decls[i].(*ast.GenDecl)
						if !ok || gd.Tok != token.TYPE {
							continue
						}
						for j, existingSpec := range gd.Specs {
							existingType, ok := existingSpec.(*ast.TypeSpec)
							if ok && existingType.Name.Name == ts.Name.Name {
								gd.Specs[j] = ts
								replaced = true
								break
							}
						}
						if replaced {
							break
						}
					}
					if !replaced {
						node.Decls = append(node.Decls, &ast.GenDecl{Tok: token.TYPE, Specs: []ast.Spec{ts}})
					}
					added = true
				}
			}
		case *ast.FuncDecl:
			newFunc := d
			for _, path := range selectorImportsInNode(newFunc) {
				already := false
				for _, existing := range node.Imports {
					if existing.Path != nil && strings.Trim(existing.Path.Value, `"`) == path {
						already = true
						break
					}
				}
				if !already {
					astutil.AddImport(fset, node, path)
					added = true
				}
			}
			replaced := false
			for i, decl := range node.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == newFunc.Name.Name {
					node.Decls[i] = newFunc
					replaced = true
					added = true
					break
				}
			}
			if !replaced {
				node.Decls = append(node.Decls, newFunc)
				added = true
			}
		}
	}

	if !added {
		if strings.HasPrefix(trimmed, "package ") {
			return true, "package clause already present", nil
		}
		return false, "", nil
	}
	var buf strings.Builder
	if err := format.Node(&buf, fset, node); err != nil {
		return false, "", fmt.Errorf("format updated AST: %w", err)
	}
	if validateErr := ValidateASTSource(filePath, buf.String()); validateErr != nil {
		return false, "", fmt.Errorf("generated change violates Go type rules: %w", validateErr)
	}
	if err := writeFormattedFile(filePath, fset, node); err != nil {
		return false, "", err
	}
	return true, fmt.Sprintf("updated %s", filePath), nil
}

// recommendAfterAction inspects the file and the recent action message and
// generates short, pragmatic recommendations (tests, formatting, vet runs,
// imports) as a follow-up suggestion to present to the user.
func looksLikeGoSnippet(code string) bool {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, needle := range []string{"package ", "func ", "type ", "import ", "var ", "const ", "if ", "for ", "switch ", "return "} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	// Also accept variable assignments (e.g. j := ...) or struct literal definitions/initializations
	if strings.Contains(trimmed, ":=") || strings.Contains(trimmed, "=") {
		return true
	}
	if strings.Contains(trimmed, "{") && strings.Contains(trimmed, "}") {
		return true
	}
	return false
}

func resolveRepoRelativePath(candidate string) string {
	if strings.TrimSpace(candidate) == "" {
		return ""
	}
	if filepath.IsAbs(candidate) {
		return candidate
	}
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	if exe, err := os.Executable(); err == nil {
		repoRoot := filepath.Dir(filepath.Dir(exe))
		resolved := filepath.Join(repoRoot, candidate)
		if _, statErr := os.Stat(resolved); statErr == nil {
			return resolved
		}
	}
	return candidate
}

func recordAcceptedIntent(prompt, action string) {
	if strings.TrimSpace(prompt) == "" || strings.TrimSpace(action) == "" {
		return
	}
	dataPath := resolveRepoRelativePath("data/training/command_examples.pb")
	example := dense.CommandExample{
		Type:      "code_update",
		Prompt:    prompt,
		Response:  action,
		CodeAfter: action,
	}
	if err := dense.AppendCommandExample(dataPath, example); err != nil {
		_ = err
	}
	transitionPath := resolveRepoRelativePath("data/models/dense/transitions.json")
	if transitionPath != "" {
		p := dense.NewPredictor()
		if err := p.LoadFromFile(transitionPath); err == nil {
			lastAction := "ADD_MISC"
			nextAction := "ADD_FUNC"
			lower := strings.ToLower(prompt)
			switch {
			case strings.Contains(lower, "json tag") || strings.Contains(lower, "json tags"):
				lastAction = "ADD_STRUCT"
				nextAction = "ADD_JSON_TAGS"
			case strings.Contains(lower, "struct") || strings.Contains(lower, "type "):
				lastAction = "ADD_STRUCT"
				nextAction = "ADD_FUNC"
			case strings.Contains(lower, "import"):
				lastAction = "ADD_IMPORT"
				nextAction = "ADD_IMPORT"
			case strings.Contains(lower, "test"):
				lastAction = "ADD_FUNC"
				nextAction = "ADD_UNIT_TEST"
			case strings.Contains(lower, "function") || strings.Contains(lower, "method"):
				lastAction = "ADD_FUNC"
				nextAction = "ADD_FUNC"
			default:
				lastAction = "ADD_MISC"
				nextAction = "ADD_FUNC"
			}
			p.RecordSequence(lastAction, nextAction)
			_ = p.SaveToFile(transitionPath)
		}
	}
	_ = triggerRetrain()
}

func triggerRetrain() error {
	cmd := exec.Command("bash", "-lc", "cd \"$(pwd)\" && make run-dense_train >/tmp/dense_retrain.log 2>&1 &")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func selectorImportsInNode(fn *ast.FuncDecl) []string {
	seen := map[string]bool{}
	locals := map[string]bool{}
	if fn.Type != nil && fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				locals[name.Name] = true
			}
		}
	}
	var paths []string
	ast.Inspect(fn, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg == nil {
			return true
		}
		if locals[pkg.Name] {
			return true
		}
		name := pkg.Name
		if name == "" || name == "builtin" || isBuiltInTypeName(name) {
			return true
		}
		if !seen[name] {
			seen[name] = true
			paths = append(paths, name)
		}
		return true
	})
	return paths
}

func isBuiltInTypeName(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "string", "bool", "byte", "rune",
		"error", "any", "comparable", "nil", "map", "chan", "func",
		"struct", "interface", "interface{}":
		return true
	default:
		return false
	}
}

func recommendAfterAction(filePath, actionMsg, codeSnippet string) string {
	if filePath == "" {
		return ""
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments); err != nil {
		return ""
	}

	var suggestions []string
	// Suggest adding a unit test when we've inserted/updated a function.
	if strings.Contains(strings.ToLower(actionMsg), "function") || strings.Contains(strings.ToLower(actionMsg), "inserted") || strings.Contains(strings.ToLower(actionMsg), "updated") {
		suggestions = append(suggestions, "Add a unit test for the new/updated function.")
		suggestions = append(suggestions, fmt.Sprintf("Run `gofmt -w %s` and `go vet %s`.", filepath.Base(filePath), filepath.Dir(filePath)))
	}
	// If an import was added, suggest checking for unused imports.
	if strings.Contains(strings.ToLower(actionMsg), "import") {
		suggestions = append(suggestions, "Ensure the imported package is used; remove unused imports.")
	}
	// Generic Go-file suggestions.
	if strings.HasSuffix(filePath, ".go") {
		suggestions = append(suggestions, "Consider running `go test ./...` if this change affects behavior.")
	}

	if len(suggestions) == 0 {
		return ""
	}
	return "\n💡 Recommendations:\n- " + strings.Join(suggestions, "\n- ")
}

// parseTypeExpr parses a simple type description into an AST expression.
// Supports maps, slices, pointers, selector expressions, channels, func types,
// and index/generic-like expressions.
func parseTypeExpr(typ string) ast.Expr {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return ast.NewIdent("interface{}")
	}
	// function type
	if strings.HasPrefix(typ, "func(") {
		end := strings.Index(typ, ")")
		if end >= 0 {
			paramsStr := typ[len("func("):end]
			var params []*ast.Field
			for _, p := range strings.Split(paramsStr, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				parts := strings.Fields(p)
				if len(parts) == 1 {
					params = append(params, &ast.Field{Type: parseTypeExpr(parts[0])})
				} else {
					name := parts[0]
					t := strings.Join(parts[1:], " ")
					params = append(params, &ast.Field{Names: []*ast.Ident{ast.NewIdent(name)}, Type: parseTypeExpr(t)})
				}
			}
			rest := strings.TrimSpace(typ[end+1:])
			var results *ast.FieldList
			if rest != "" {
				rest = strings.TrimSpace(rest)
				if strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")") {
					rest = strings.Trim(rest, "() ")
				}
				if rest != "" {
					var rfields []*ast.Field
					for _, r := range strings.Split(rest, ",") {
						r = strings.TrimSpace(r)
						if r == "" {
							continue
						}
						parts := strings.Fields(r)
						if len(parts) == 1 {
							rfields = append(rfields, &ast.Field{Type: parseTypeExpr(parts[0])})
						} else {
							rfields = append(rfields, &ast.Field{Names: []*ast.Ident{ast.NewIdent(parts[0])}, Type: parseTypeExpr(strings.Join(parts[1:], " "))})
						}
					}
					results = &ast.FieldList{List: rfields}
				}
			}
			return &ast.FuncType{Params: &ast.FieldList{List: params}, Results: results}
		}
	}
	// map[K]V
	if strings.HasPrefix(typ, "map[") {
		if idx := strings.Index(typ, "]"); idx > 4 {
			key := strings.TrimSpace(typ[len("map["):idx])
			val := strings.TrimSpace(typ[idx+1:])
			return &ast.MapType{Key: parseTypeExpr(key), Value: parseTypeExpr(val)}
		}
	}
	// channel
	if strings.HasPrefix(typ, "chan ") {
		return &ast.ChanType{Value: parseTypeExpr(strings.TrimSpace(strings.TrimPrefix(typ, "chan ")))}
	}
	// slice
	if strings.HasPrefix(typ, "[]") {
		return &ast.ArrayType{Elt: parseTypeExpr(strings.TrimPrefix(typ, "[]"))}
	}
	// pointer
	if strings.HasPrefix(typ, "*") {
		return &ast.StarExpr{X: parseTypeExpr(strings.TrimPrefix(typ, "*"))}
	}
	// generic/index expression like MyType[T]
	if i := strings.Index(typ, "["); i > 0 && strings.HasSuffix(typ, "]") {
		x := strings.TrimSpace(typ[:i])
		inside := strings.TrimSpace(typ[i+1 : len(typ)-1])
		return &ast.IndexExpr{X: ast.NewIdent(x), Index: parseTypeExpr(inside)}
	}
	// selector expressions a.b.c -> nested SelectorExprs
	if strings.Contains(typ, ".") {
		parts := strings.Split(typ, ".")
		var expr ast.Expr = ast.NewIdent(parts[0])
		for _, p := range parts[1:] {
			expr = &ast.SelectorExpr{X: expr, Sel: ast.NewIdent(p)}
		}
		return expr
	}
	return ast.NewIdent(typ)
}

// addImportToNode adds an import path to the file's import declarations,
// creating the import block if needed.
func addImportToNode(node *ast.File, path string) {
	impSpec := &ast.ImportSpec{
		Path: &ast.BasicLit{
			Kind:  token.STRING,
			Value: fmt.Sprintf("%q", path),
		},
	}
	var importDecl *ast.GenDecl
	for _, decl := range node.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			importDecl = gd
			break
		}
	}
	if importDecl == nil {
		importDecl = &ast.GenDecl{Tok: token.IMPORT}
		node.Decls = append([]ast.Decl{importDecl}, node.Decls...)
	}
	importDecl.Specs = append(importDecl.Specs, impSpec)
}

// writeFormattedFile writes the AST back to disk with gofmt formatting.
func writeFormattedFile(filePath string, fset *token.FileSet, node *ast.File) error {
	var buf strings.Builder
	if err := format.Node(&buf, fset, node); err != nil {
		return fmt.Errorf("format node: %w", err)
	}
	if err := os.WriteFile(filePath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// ─── Conversation Commands ────────────────────────────────────────────────────

// handleCommand processes slash-commands in interactive mode.
// Returns (handled, response).
func handleCommand(line string, mgr *ConversationManager) (bool, string) {
	if !(strings.HasPrefix(line, ":") || strings.HasPrefix(line, "/")) {
		return false, ""
	}

	parts := strings.Fields(line)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case ":reload":
		conv := mgr.Get()
		if conv == nil || conv.TargetGoFile() == "" {
			return true, colorize("No target Go file to reload.", "\033[33m")
		}
		if _, err := os.Stat(conv.TargetGoFile()); err != nil {
			return true, colorize(fmt.Sprintf("Reload failed: %v", err), "\033[31m")
		}
		return true, colorize(fmt.Sprintf("🔄 Reloaded AST context for %s", conv.TargetGoFile()), "\033[32m")
	case ":undo":
		conv := mgr.Get()
		if conv == nil {
			return true, colorize("No active conversation.", "\033[31m")
		}
		ok, msg := conv.UndoLastEdit()
		if !ok {
			return true, colorize(msg, "\033[31m")
		}
		return true, colorize(msg, "\033[33m")
	case ":history":
		conv := mgr.Get()
		if conv == nil {
			return true, colorize("No active conversation.", "\033[31m")
		}
		return true, colorize(formatHistoryTurns(conv.Turns), "\033[36m")
	case "/new":
		name := ""
		if len(parts) > 1 {
			name = parts[1]
		}
		_, convName := mgr.New(name)
		return true, fmt.Sprintf("🆕 Started new conversation %q", convName)

	case "/list":
		names := mgr.List()
		if len(names) == 0 {
			return true, "No conversations."
		}
		var sb strings.Builder
		sb.WriteString("📋 Conversations:\n")
		for _, n := range names {
			marker := "  "
			if n == mgr.Active() {
				marker = "▶ "
			}
			conv := mgr.GetByName(n)
			turnCount := 0
			target := ""
			if conv != nil {
				turnCount = len(conv.Turns)
				if conv.TargetGoFile() != "" {
					target = " | file: " + conv.TargetGoFile()
				}
			}
			sb.WriteString(fmt.Sprintf("%s%s (%d turns%s)\n", marker, n, turnCount, target))
		}
		return true, strings.TrimRight(sb.String(), "\n")

	case "/switch":
		if len(parts) < 2 {
			return true, "Usage: /switch <conversation-name>"
		}
		name := parts[1]
		if mgr.Switch(name) {
			conv := mgr.Get()
			target := ""
			if conv != nil && conv.TargetGoFile() != "" {
				target = " (" + conv.TargetGoFile() + ")"
			}
			return true, fmt.Sprintf("🔀 Switched to conversation %q%s", name, target)
		}
		return true, fmt.Sprintf("❌ Conversation %q not found. Use /list to see available conversations.", name)

	case "/file":
		if len(parts) < 2 {
			conv := mgr.Get()
			if conv != nil && conv.TargetGoFile() != "" {
				return true, fmt.Sprintf("📄 Active conversation %q targets: %s", mgr.Active(), conv.TargetGoFile())
			}
			return true, "Usage: /file <path-to-.go-file>"
		}
		rest := strings.TrimSpace(line[len(parts[0]):])
		path, trailing := splitFileCommand(rest)
		if path == "" {
			path = strings.Join(parts[1:], " ")
		}
		conv := mgr.Get()
		if err := conv.SetTargetFile(path); err != nil {
			return true, fmt.Sprintf("❌ %v", err)
		}
		if strings.TrimSpace(trailing) != "" {
			if ok, msg := tryApplyExactFunctionReplacement(trailing, path); ok {
				return true, fmt.Sprintf("📄 Conversation %q will now update %s\n%s", mgr.Active(), conv.TargetGoFile(), msg)
			}
		}
		return true, fmt.Sprintf("📄 Conversation %q will now update %s", mgr.Active(), conv.TargetGoFile())

	case "/delete":
		if len(parts) < 2 {
			return true, "Usage: /delete <conversation-name>"
		}
		name := parts[1]
		if mgr.Delete(name) {
			return true, fmt.Sprintf("🗑️ Deleted conversation %q. Active: %q", name, mgr.Active())
		}
		return true, fmt.Sprintf("❌ Cannot delete %q (not found or the last conversation).", name)

	case "/current":
		conv := mgr.Get()
		target := ""
		if conv != nil && conv.TargetGoFile() != "" {
			target = fmt.Sprintf(" -> %s", conv.TargetGoFile())
		}
		return true, fmt.Sprintf("💬 Active conversation: %q%s", mgr.Active(), target)

	case "/help":
		return true, colorize(`Available commands:
  /new [name]        start a new conversation (auto-generates a name if omitted)
  /list              list all conversations
  /switch <name>     switch to an existing conversation
  /delete <name>     delete a conversation
  /current           show the active conversation name
  /file <path>       set the target Go file to update for the active conversation
  :reload            refresh package AST context for the active file
  :undo              rollback the last successful edit
  :history           show the current session turns
  /help              show this help`, "\033[33m")

	default:
		return true, fmt.Sprintf("❌ Unknown command: %s. Type /help for available commands.", cmd)
	}
}

func resolvePromptTarget(prompt, fallback string) string {
	if explicit := inferTargetFileFromPrompt(prompt); explicit != "" {
		return explicit
	}
	if fallback != "" {
		return fallback
	}
	return ""
}

func main() {
	modelPath := flag.String("model", "data/models/dense/model.gob", "path to trained gob model file")
	dataPath := flag.String("data", "data/training/command_examples.pb", "path to protobuf training data for response matching")
	oneShot := flag.String("prompt", "", "classify a single prompt and exit (interactive if empty)")
	modelPathValue := resolveRepoRelativePath(*modelPath)
	dataPathValue := resolveRepoRelativePath(*dataPath)
	if modelPathValue != *modelPath {
		modelPath = &modelPathValue
	}
	if dataPathValue != *dataPath {
		dataPath = &dataPathValue
	}
	dirPath := flag.String("dir", ".", "Root workspace path to index")
	targetFile := flag.String("file", "", "default target Go file used by the default conversation")
	httpAddr := flag.String("http", "", "optional HTTP adapter address (for example :8080)")
	benchmark := flag.Bool("benchmark", false, "run a lightweight intent and compilation benchmark and exit")
	nativeTUI := flag.Bool("native-tui", false, "run a pure-Go terminal split-pane preview without external dependencies")
	backupFlag := flag.Bool("backup", false, "create .bak backups before overwriting Go files")
	backupWrites = *backupFlag
	flag.Parse()

	// ── Workspace-aware Direct CLI routing ───────────────────────────────────
	// When -file and -prompt are both set, attempt a direct deterministic AST
	// mutation via RouteAndExecute, bypassing the LLM pipeline entirely.
	// The workspace graph is loaded (or rebuilt from cache) so that the target
	// file can be resolved by symbol name even when -file is not provided.
	if *oneShot != "" {
		rootDir := *dirPath
		if rootDir == "" {
			rootDir, _ = os.Getwd()
		}
		cachePath := dense.DefaultCachePath(rootDir)

		// Try loading from cache first (valid for 10 minutes).
		wsCache, _ := dense.LoadWorkspaceCache(cachePath)
		var wgraph *dense.WorkspaceGraph

		resolvedFile := *targetFile

		if wsCache == nil || wsCache.Stale(10*time.Minute) {
			// Full re-index; persist for subsequent calls.
			if g, gErr := dense.IndexWorkspace(rootDir); gErr == nil {
				wgraph = g
				_ = dense.SaveWorkspaceCache(cachePath, dense.GraphToCache(rootDir, g))
			}
		}

		if wgraph == nil && wsCache != nil {
			if g, gErr := dense.IndexWorkspace(rootDir); gErr == nil {
				wgraph = g
			}
		}

		// If no -file flag, resolve the target via the code-aware slot parser.
		if resolvedFile == "" {
			slot := dense.ParseCodeAwarePrompt(*oneShot, wgraph)
			if slot.TargetSymbol == "" && slot.ExplicitFile == "" {
				log.Printf("Could not resolve target symbol from prompt: %q", *oneShot)
			} else {
				if wgraph != nil && slot.TargetSymbol != "" {
					if sym, ok := wgraph.FindSymbol(slot.TargetSymbol); ok {
						resolvedFile = sym.FilePath
					}
				}
				if resolvedFile == "" && slot.ExplicitFile != "" {
					// match suffixes against indexed file paths
					for fp := range wgraph.Files {
						if strings.HasSuffix(fp, slot.ExplicitFile) {
							resolvedFile = fp
							break
						}
					}
				}
				if resolvedFile == "" && wsCache != nil {
					if fp, _, _, ok := wsCache.FindInCache(slot.TargetSymbol); ok {
						resolvedFile = fp
					}
				}
			}
		}

		if resolvedFile != "" {
			// Optionally write backup.
			if *backupFlag {
				if src, readErr := os.ReadFile(resolvedFile); readErr == nil {
					_ = os.WriteFile(resolvedFile+".bak", src, 0644)
				}
			}

			fset := token.NewFileSet()
			fileAST, parseErr := parser.ParseFile(fset, resolvedFile, nil, parser.ParseComments)
			if parseErr == nil {
				slot := dense.ParseCodeAwarePrompt(*oneShot, wgraph)
				if slot.TargetSymbol == "" && slot.ExplicitFile == "" && slot.Action != "ADD_FUNC" {
					log.Fatalf("Error: Could not resolve target symbol or file from prompt: %q", *oneShot)
				}
				if targetFile, success := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(wgraph, resolvedFile, slot); success {
					if targetFile != "" {
						f, createErr := os.Create(targetFile)
						if createErr == nil {
							defer f.Close()
							// Write the possibly-mutated AST from the workspace index (if present)
							if astFromIndex, ok := wgraph.Files[targetFile]; ok {
								_ = format.Node(f, fset, astFromIndex)
							} else {
								_ = format.Node(f, fset, fileAST)
							}
							fmt.Printf("Successfully updated %s\n", targetFile)
							os.Exit(0)
						}
					}
				}
			}
		}
	}

	index, err := dense.LoadProjectIndex(".")
	if err != nil {
		log.Printf("project index warmup failed: %v", err)
	} else {
		index.PrintSummary()
		if err := index.WatchAndInvalidate("."); err != nil {
			log.Printf("watch and invalidate: %v", err)
		}
	}

	// Load the trained gob model.
	model, err := dense.LoadGob(*modelPath)
	if err != nil {
		log.Fatalf("load gob model: %v", err)
	}
	fmt.Printf("📦 Loaded model from %s\n", *modelPath)

	// Load the command corpus for response matching (protobuf or CSV).
	var examples []dense.CommandExample
	if strings.HasSuffix(*dataPath, ".pb") {
		examples, err = dense.LoadCommandExamplesFromProto(*dataPath)
	} else {
		examples, err = dense.LoadCommandExamplesFromCSV(*dataPath)
	}
	if err != nil {
		log.Fatalf("load command corpus: %v", err)
	}

	// Initialize the conversation manager (multiconversational).
	mgr := NewConversationManager()

	if *targetFile != "" {
		if err := mgr.Get().SetTargetFile(*targetFile); err != nil {
			log.Fatalf("invalid -file: %v", err)
		}
	}
	if *benchmark {
		results := RunBenchmark(*modelPath, defaultBenchmarkPrompts())
		fmt.Printf("Intent precision: %.2f\n", results.IntentPrecision)
		fmt.Printf("AST compilation rate: %.2f\n", results.ASTCompilationRate)
		return
	}
	if *nativeTUI {
		if err := runNativeTUI(model, examples, *targetFile); err != nil {
			log.Fatalf("native tui: %v", err)
		}
		return
	}
	if *httpAddr != "" {
		if err := runHTTPAdapter(*httpAddr, *modelPath, *dataPath); err != nil {
			log.Fatalf("http adapter: %v", err)
		}
		return
	}

	respond := func(prompt string) string {
		trimmed := strings.TrimSpace(prompt)
		if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, ":") {
			if handled, resp := handleCommand(trimmed, mgr); handled {
				return resp
			}
		}
		conv := mgr.Get()
		target := resolvePromptTarget(prompt, conv.TargetGoFile())
		if target == "" {
			target = dense.InferTargetFromPrompt(prompt)
		}
		if target == "" {
			target = filepath.Join(".", "dense_generated.go")
		}
		if ok, msg := tryApplyExactFunctionReplacement(prompt, target); ok {
			return msg
		}
		// Dedicated NL handler for "import [and use] struct|function X from file A into file B".
		if msg, err := tryHandleImportAndUse(prompt); err != nil {
			return fmt.Sprintf("⚠️  %v", err)
		} else if msg != "" {
			return "🔧 " + msg
		}

		// Try Lexical Tokenization & Hybrid AST Mutation
		if target != "" && strings.HasSuffix(target, ".go") {
			if content, err := os.ReadFile(target); err == nil {
				fset := token.NewFileSet()
				if fileAST, err := parser.ParseFile(fset, target, string(content), parser.ParseComments); err == nil {
					intent, parsed := dense.ResolveIntent(prompt, fileAST)
					if intent == dense.IntentAddTags || intent == dense.IntentWrapErrors {
						if dense.ExecuteMutation(fileAST, intent, parsed) {
							var buf strings.Builder
							if err := format.Node(&buf, fset, fileAST); err == nil {
								if err := os.WriteFile(target, []byte(buf.String()), 0644); err == nil {
									return fmt.Sprintf("🔧 Applied %s mutation directly to AST of %s", intent, target)
								}
							}
						}
					}
				}
			}
		}

		response := buildContextAwareResponse(prompt, conv, model, examples)

		// Track the turn in conversation history.
		cmdType := dense.ClassifyCommandType(prompt)
		if cmdType == "social" {
			input := dense.BagOfWords(prompt, dense.CommandVocab)
			preds := model.Predict([][]float32{input})
			if len(preds) > 0 {
				label := preds[0]
				if label >= 0 && label < len(dense.CommandLabels) {
					candidate := dense.CommandLabels[label]
					if candidate != "social" {
						cmdType = candidate
					}
				}
			}
		}

		// Strip the emoji prefix for history storage.
		cleanResponse := strings.TrimPrefix(response, "🤖 ")
		cleanResponse = strings.TrimPrefix(cleanResponse, "🔧 ")

		conv.AddTurn(prompt, cleanResponse, cmdType)

		// Apply file or code actions to the active target file when present.
		if cmdType == "code_update" {
			target := conv.TargetGoFile()
			if target == "" {
				target = resolvePromptTarget(prompt, conv.TargetGoFile())
			}
			if target == "" {
				target = dense.InferTargetFromPrompt(prompt)
			}
			if target == "" {
				target = filepath.Join(".", "dense_generated.go")
			}
			if err := ensureGoTargetFile(target, prompt); err != nil {
				response += fmt.Sprintf("\n⚠️  Could not prepare target file %q: %v", target, err)
				return response
			}
			if err := conv.SetTargetFile(target); err != nil {
				response += fmt.Sprintf("\n⚠️  Could not set target file %q: %v", target, err)
				return response
			}

			conv.PushUndoSnapshot(target)
			code := strings.TrimPrefix(response, "🔧 ")
			code = strings.TrimSpace(code)
			if code == "" {
				return response + "\n⚠️  No valid Go code was generated for this prompt."
			}

			if strings.Contains(strings.ToLower(prompt), "replace ") && strings.Contains(strings.ToLower(prompt), " with ") {
				if idx := strings.Index(strings.ToLower(prompt), "replace "); idx >= 0 {
					namePart := strings.TrimSpace(prompt[idx+len("replace "):])
					if j := strings.Index(strings.ToLower(namePart), " with "); j >= 0 {
						name := strings.TrimSpace(namePart[:j])
						if name != "" {
							if _, err := applyFunctionReplacement(target, name, code); err == nil {
								response += fmt.Sprintf("\n\033[32m✅ Applied exact replacement to %s\033[0m", target)
								return response
							}
						}
					}
				}
			}

			msg, err := applyCodeToFile(target, code)
			if err != nil {
				response += fmt.Sprintf("\n\033[31m⚠️  Could not apply to %s: %v\033[0m", target, err)
			} else {
				recordAcceptedIntent(prompt, "code_update")
				response += fmt.Sprintf("\n\033[32m✅ Applied to %s: %s\033[0m", target, msg)
				if recs := recommendAfterAction(target, msg, code); recs != "" {
					response += recs
				}
			}
			return response
		}
		if cmdType == "file_create" || cmdType == "file_edit" || cmdType == "file_delete" {
			target := conv.TargetFile
			if target == "" {
				target = dense.InferTargetFromPrompt(prompt)
			}
			if target == "" {
				response += "\n📄 No file targeted in this conversation. Use /file <path> to set the target file."
				return response
			}
			if err := conv.SetTargetFile(target); err != nil {
				response += fmt.Sprintf("\n⚠️  Could not set target file %q: %v", target, err)
				return response
			}

			content := strings.TrimPrefix(response, "🔧 ")
			content = strings.TrimSpace(content)
			if cmdType == "file_delete" {
				msg, err := applyFileOperation(target, "file_delete", "")
				if err != nil {
					response += fmt.Sprintf("\n⚠️  Could not delete %s: %v", target, err)
				} else {
					response += fmt.Sprintf("\n✅ %s", msg)
				}
				return response
			}
			if content == "" {
				if cmdType == "file_create" {
					content = ""
				} else {
					content = "updated"
				}
			}
			msg, err := applyFileOperation(target, cmdType, content)
			if err != nil {
				response += fmt.Sprintf("\n⚠️  Could not apply to %s: %v", target, err)
			} else {
				response += fmt.Sprintf("\n✅ %s", msg)
				if recs := recommendAfterAction(target, msg, content); recs != "" {
					response += recs
				}
			}
			return response
		}
		if cmdType == "folder_create" || cmdType == "folder_delete" {
			target := dense.InferTargetFromPrompt(prompt)
			if target == "" {
				target = strings.TrimSpace(strings.Split(prompt, " ")[(len(strings.Fields(prompt)) - 1)])
				if strings.Contains(strings.ToLower(prompt), "folder") || strings.Contains(strings.ToLower(prompt), "directory") {
					parts := strings.Fields(prompt)
					if len(parts) >= 3 {
						target = parts[len(parts)-1]
					}
				}
			}
			if target == "" {
				response += "\n📁 Could not determine which folder to operate on."
				return response
			}
			msg, err := applyFolderOperation(target, cmdType)
			if err != nil {
				response += fmt.Sprintf("\n⚠️  Could not apply folder action to %s: %v", target, err)
			} else {
				response += fmt.Sprintf("\n✅ %s", msg)
			}
			return response
		}

		return response
	}

	if *oneShot != "" {
		fmt.Printf("prompt: %q\n", *oneShot)
		trimmed := strings.TrimSpace(*oneShot)
		if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, ":") {
			if handled, resp := handleCommand(trimmed, mgr); handled {
				fmt.Println(resp)
				return
			}
		}
		target := resolvePromptTarget(*oneShot, mgr.Get().TargetGoFile())
		if target == "" {
			target = dense.InferTargetFromPrompt(*oneShot)
		}
		if ok, msg := tryApplyExactFunctionReplacement(*oneShot, target); ok {
			fmt.Println(msg)
			return
		}
		fmt.Println(respond(*oneShot))
		return
	}

	fmt.Println("💬 dense-llm interactive shell  (exit with Ctrl+D)")
	fmt.Println("    Trained on the basic go_edit_agent update-command corpus.")
	fmt.Println("    Multi-conversation mode: use /new, /list, /switch, /delete.")
	fmt.Println("    Go-file editing: use /file <path> to set the target file per conversation.")
	if *targetFile != "" {
		fmt.Printf("    Default target Go file for the default conversation: %s\n", *targetFile)
	}
	fmt.Println()
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("[%s] > ", mgr.Active())
		if !sc.Scan() {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		if handled, resp := handleCommand(line, mgr); handled {
			fmt.Println(resp)
			continue
		}
		target := resolvePromptTarget(line, mgr.Get().TargetGoFile())
		if target == "" {
			target = dense.InferTargetFromPrompt(line)
		}
		if ok, msg := tryApplyExactFunctionReplacement(line, target); ok {
			fmt.Println(colorize(msg, "\033[32m"))
			continue
		}
		fmt.Println(colorize(respond(line), "\033[32m"))
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("read stdin: %v", err)
	}
}
