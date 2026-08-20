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
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/golangast/dense/internal/ai/dense"
)

// ChatTurn is a single user/assistant exchange in the conversation history.
type ChatTurn struct {
	User      string
	Assistant string
	Type      string // "social" or "code_update"
}

// Conversation tracks the multi-turn dialogue state for a single thread.
type Conversation struct {
	ID         string
	Turns      []ChatTurn
	TargetFile string // Go file this conversation applies code updates to
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
func functionSnippetFromPrompt(prompt string) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
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

// predictIntent extracts a structured intent from the prompt using simple
// heuristics (and falling back to example matching). The model isn't asked
// to produce raw code; instead it guides which action and parameters to take.
func predictIntent(prompt string, conv *Conversation, model *dense.DenseModel, examples []dense.CommandExample) Intent {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	// Import intent
	if strings.Contains(lower, "import ") || strings.Contains(lower, "add import") {
		// try to extract a quoted import path
		if i1 := strings.Index(prompt, "\""); i1 >= 0 {
			if i2 := strings.Index(prompt[i1+1:], "\""); i2 >= 0 {
				pkg := prompt[i1+1 : i1+1+i2]
				if pkg != "" {
					return Intent{Action: "ADD_IMPORT", Name: pkg}
				}
			}
		}
		// fallback: take last token
		parts := strings.Fields(prompt)
		if len(parts) > 0 {
			last := strings.Trim(parts[len(parts)-1], " ,.")
			return Intent{Action: "ADD_IMPORT", Name: last}
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
			it := Intent{Action: "ADD_FUNC", Name: strings.Title(strings.TrimSpace(name)), Params: params, Receiver: receiver, Returns: rets}
			if recv != "" && it.Receiver == "" {
				it.Receiver = recv
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
	if strings.Contains(lower, "err != nil") || strings.Contains(lower, "add error check") || strings.Contains(lower, "return on error") {
		// choose default target var 'err'
		ret := ""
		if strings.Contains(lower, "return nil") || strings.Contains(lower, "return nil,") {
			ret = "nil"
		} else if strings.Contains(lower, "return \"\"") || strings.Contains(lower, "empty string") {
			ret = `""`
		} else if strings.Contains(lower, "return 0") {
			ret = "0"
		}
		return Intent{Action: "ADD_ERROR_CHECK", Target: "err", Ret: ret}
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
	if strings.Contains(lower, "create struct") || strings.Contains(lower, "create type") || strings.Contains(lower, "add struct") {
		// take the last field as the type name if present
		parts := strings.Fields(prompt)
		if len(parts) > 0 {
			last := strings.Trim(parts[len(parts)-1], " ,.")
			return Intent{Action: "ADD_TYPE", Name: last}
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
	return Intent{}
}

// renderIntentToCode deterministically converts an Intent to a Go code
// snippet using go/ast and formatting, ensuring syntactic validity.
func renderIntentToCode(intent Intent) (string, error) {
	// No nested parseTypeExpr here; use package-level parseTypeExpr helper.
	switch intent.Action {
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
			// create a return of zero values based on simple types
			var exprs []ast.Expr
			for _, r := range intent.Returns {
				switch r {
				case "int", "int64", "int32":
					exprs = append(exprs, &ast.BasicLit{Kind: token.INT, Value: "0"})
				case "string":
					exprs = append(exprs, &ast.BasicLit{Kind: token.STRING, Value: `""`})
				case "bool":
					exprs = append(exprs, ast.NewIdent("false"))
				default:
					// assume nilable
					exprs = append(exprs, ast.NewIdent("nil"))
				}
			}
			ret := &ast.ReturnStmt{Results: exprs}
			fn.Body.List = append(fn.Body.List, ret)
		} else {
			// default TODO comment
			fn.Body.List = append(fn.Body.List, &ast.ExprStmt{X: &ast.BasicLit{Kind: token.STRING, Value: `"TODO: implement"`}})
		}
		// Receiver
		if strings.TrimSpace(intent.Receiver) != "" {
			r := strings.TrimSpace(intent.Receiver)
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

		// Render the node to source.
		var sb strings.Builder
		if err := format.Node(&sb, token.NewFileSet(), fn); err != nil {
			return "", err
		}
		// Ensure trailing newline
		out := sb.String()
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out, nil
	case "ADD_IMPORT":
		if intent.Name == "" {
			return "", nil
		}
		// return a single import spec
		out := fmt.Sprintf("import %q\n", intent.Name)
		return out, nil
	case "ADD_TYPE":
		if intent.Name == "" {
			return "", nil
		}
		// type Name struct {}
		ts := &ast.GenDecl{
			Tok: token.TYPE,
			Specs: []ast.Spec{
				&ast.TypeSpec{
					Name: ast.NewIdent(intent.Name),
					Type: &ast.StructType{Fields: &ast.FieldList{}},
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
				Params: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("*testing.T")}}},
			},
			Body: &ast.BlockStmt{List: []ast.Stmt{
				&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("t.Skip"), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"TODO: implement"`}}}},
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
		// Build: if err != nil { return <Ret> }
		cond := &ast.BinaryExpr{
			X:  ast.NewIdent(intent.Target),
			Op: token.NEQ,
			Y:  ast.NewIdent("nil"),
		}
		var retStmt ast.Stmt
		if intent.Ret != "" {
			// return <Ret>
			retStmt = &ast.ReturnStmt{Results: []ast.Expr{&ast.Ident{Name: intent.Ret}}}
		} else {
			// generic: return
			retStmt = &ast.ReturnStmt{}
		}
		blk := &ast.BlockStmt{List: []ast.Stmt{retStmt}}
		ifStmt := &ast.IfStmt{Cond: cond, Body: blk}
		var sb strings.Builder
		if err := format.Node(&sb, token.NewFileSet(), ifStmt); err != nil {
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

func buildContextAwareResponse(prompt string, conv *Conversation, model *dense.DenseModel, examples []dense.CommandExample) string {
	// Inspect the target Go file (if any) to gather structural context that
	// can influence classification and response construction. This allows the
	// assistant to prefer edits over creates when the target already contains
	// matching symbols, or to prefer code_update when a function exists.
	lower := strings.ToLower(strings.TrimSpace(prompt))
	target := ""
	if conv != nil {
		target = conv.TargetGoFile()
	}
	var hasFunc = map[string]bool{}
	var hasType = map[string]bool{}
	var hasImport = map[string]bool{}
	if target != "" {
		if fi, err := os.Stat(target); err == nil && !fi.IsDir() && strings.HasSuffix(target, ".go") {
			fset := token.NewFileSet()
			if node, err := parser.ParseFile(fset, target, nil, parser.ParseComments); err == nil {
				// collect functions, types and imports
				for _, decl := range node.Decls {
					if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil {
						hasFunc[strings.ToLower(fn.Name.Name)] = true
					}
					if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
						for _, spec := range gd.Specs {
							if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name != nil {
								hasType[strings.ToLower(ts.Name.Name)] = true
							}
						}
					}
				}
				for _, imp := range node.Imports {
					if imp.Path != nil {
						p := strings.Trim(imp.Path.Value, `"`)
						hasImport[strings.ToLower(p)] = true
					}
				}
			}
		}
	}

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
		if snippet := functionSnippetFromPrompt(prompt); snippet != "" {
			return "🔧 " + snippet
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

	// Read the current file content.
	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	// Try AST-based insertion first.
	applied, msg, err := applyCodeViaAST(absPath, string(content), code)
	if err == nil && applied {
		return msg, nil
	}

	// Fallback: append the snippet to the end of the file.
	newContent := string(content)
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	newContent += code + "\n"

	// Verify the result is still valid Go.
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, absPath, newContent, parser.ParseComments); err != nil {
		return "", fmt.Errorf("appended code produces invalid Go: %v", err)
	}

	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	// Run gofmt.
	exec.Command("gofmt", "-w", absPath).Run()

	return fmt.Sprintf("appended code to %s", absPath), nil
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

	// Handle import statements.
	if strings.HasPrefix(trimmed, "import ") {
		importPath := strings.Trim(strings.TrimPrefix(trimmed, "import "), `"`)
		importPath = strings.TrimSpace(importPath)
		if importPath == "" {
			return false, "", fmt.Errorf("empty import path")
		}
		// Check if already imported.
		for _, imp := range node.Imports {
			if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == importPath {
				return true, fmt.Sprintf("import %q already present", importPath), nil
			}
		}
		// Add the import.
		impSpec := &ast.ImportSpec{
			Path: &ast.BasicLit{
				Kind:  token.STRING,
				Value: fmt.Sprintf("%q", importPath),
			},
		}
		// Find or create the import declaration.
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
		if err := writeFormattedFile(filePath, fset, node); err != nil {
			return false, "", err
		}
		return true, fmt.Sprintf("added import %q to %s", importPath, filePath), nil
	}

	// Handle function declarations.
	if strings.HasPrefix(trimmed, "func ") {
		// Parse the function code.
		src := fmt.Sprintf("package main\n\n%s", trimmed)
		funcFset := token.NewFileSet()
		funcNode, err := parser.ParseFile(funcFset, "", src, parser.ParseComments)
		if err != nil {
			return false, "", fmt.Errorf("cannot parse function code: %v", err)
		}
		var newFunc *ast.FuncDecl
		for _, decl := range funcNode.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				newFunc = fn
				break
			}
		}
		if newFunc == nil {
			return false, "", fmt.Errorf("no function declaration found in code")
		}

		// Add imports from the function code.
		for _, imp := range funcNode.Imports {
			if imp.Path != nil {
				path := strings.Trim(imp.Path.Value, `"`)
				// Check if already imported.
				already := false
				for _, existing := range node.Imports {
					if existing.Path != nil && strings.Trim(existing.Path.Value, `"`) == path {
						already = true
						break
					}
				}
				if !already {
					addImportToNode(node, path)
				}
			}
		}

		// Check if function already exists; if so, replace it in place.
		replaced := false
		for i, decl := range node.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == newFunc.Name.Name {
				node.Decls[i] = newFunc
				replaced = true
				break
			}
		}
		if !replaced {
			// Append the function.
			node.Decls = append(node.Decls, newFunc)
		}

		if err := writeFormattedFile(filePath, fset, node); err != nil {
			return false, "", err
		}
		action := "inserted"
		if replaced {
			action = "updated"
		}
		return true, fmt.Sprintf("%s function %q in %s", action, newFunc.Name.Name, filePath), nil
	}

	// Handle struct / type declarations (struct, interface, and any named type).
	if strings.HasPrefix(trimmed, "type ") {
		// Parse the type code.
		src := fmt.Sprintf("package main\n\n%s", trimmed)
		typeFset := token.NewFileSet()
		typeNode, err := parser.ParseFile(typeFset, "", src, parser.ParseComments)
		if err != nil {
			return false, "", fmt.Errorf("cannot parse type code: %v", err)
		}
		var newType *ast.TypeSpec
		for _, decl := range typeNode.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, spec := range gd.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						newType = ts
						break
					}
				}
			}
		}
		if newType == nil {
			return false, "", fmt.Errorf("no type declaration found in code")
		}

		// Check if type already exists; if so, replace it in place.
		replaced := false
		for i := range node.Decls {
			gd, ok := node.Decls[i].(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for j, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if ok && ts.Name.Name == newType.Name.Name {
					gd.Specs[j] = newType
					replaced = true
					break
				}
			}
			if replaced {
				break
			}
		}
		if !replaced {
			// Append the type declaration.
			newDecl := &ast.GenDecl{
				Tok:   token.TYPE,
				Specs: []ast.Spec{newType},
			}
			node.Decls = append(node.Decls, newDecl)
		}

		if err := writeFormattedFile(filePath, fset, node); err != nil {
			return false, "", err
		}
		action := "inserted"
		if replaced {
			action = "updated"
		}
		return true, fmt.Sprintf("%s type %q in %s", action, newType.Name.Name, filePath), nil
	}

	// Handle package clause.
	if strings.HasPrefix(trimmed, "package ") {
		// Package clause is already present in the file; no-op.
		return true, "package clause already present", nil
	}

	// Handle const/var declaration blocks.
	if strings.HasPrefix(trimmed, "const (") || strings.HasPrefix(trimmed, "var (") {
		// Parse the block.
		src := fmt.Sprintf("package main\n\n%s", trimmed)
		blockFset := token.NewFileSet()
		blockNode, err := parser.ParseFile(blockFset, "", src, parser.ParseComments)
		if err != nil {
			return false, "", fmt.Errorf("cannot parse declaration block: %v", err)
		}
		var newDecl *ast.GenDecl
		for _, decl := range blockNode.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok {
				newDecl = gd
				break
			}
		}
		if newDecl == nil {
			return false, "", fmt.Errorf("no declaration block found in code")
		}
		node.Decls = append(node.Decls, newDecl)
		if err := writeFormattedFile(filePath, fset, node); err != nil {
			return false, "", err
		}
		return true, fmt.Sprintf("inserted declaration block into %s", filePath), nil
	}

	// For simple statements, we can't easily determine where to insert them via
	// AST. Return not-applied so the caller falls back to appending.
	return false, "", nil
}

// recommendAfterAction inspects the file and the recent action message and
// generates short, pragmatic recommendations (tests, formatting, vet runs,
// imports) as a follow-up suggestion to present to the user.
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
	if !strings.HasPrefix(line, "/") {
		return false, ""
	}

	parts := strings.Fields(line)
	cmd := strings.ToLower(parts[0])

	switch cmd {
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
		path := strings.Join(parts[1:], " ")
		conv := mgr.Get()
		if err := conv.SetTargetFile(path); err != nil {
			return true, fmt.Sprintf("❌ %v", err)
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
		return true, `Available commands:
  /new [name]        start a new conversation (auto-generates a name if omitted)
  /list              list all conversations
  /switch <name>     switch to an existing conversation
  /delete <name>     delete a conversation
  /current           show the active conversation name
  /file <path>       set the target Go file to update for the active conversation
  /help              show this help`

	default:
		return true, fmt.Sprintf("❌ Unknown command: %s. Type /help for available commands.", cmd)
	}
}

func main() {
	modelPath := flag.String("model", "data/models/dense/model.gob", "path to trained gob model file")
	dataPath := flag.String("data", "data/training/command_examples.pb", "path to protobuf training data for response matching")
	oneShot := flag.String("prompt", "", "classify a single prompt and exit (interactive if empty)")
	targetFile := flag.String("file", "", "default target Go file used by the default conversation")
	flag.Parse()

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

	// Apply the global -file flag to the default conversation so live editing
	// works even before any /file command is issued.
	if *targetFile != "" {
		if err := mgr.Get().SetTargetFile(*targetFile); err != nil {
			log.Fatalf("invalid -file: %v", err)
		}
	}

	respond := func(prompt string) string {
		conv := mgr.Get()
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
				target = dense.InferTargetFromPrompt(prompt)
			}
			if target == "" {
				response += "\n📄 No Go file targeted in this conversation. Use /file <path-to-.go-file> to set the file to update."
				return response
			}
			if err := conv.SetTargetFile(target); err != nil {
				response += fmt.Sprintf("\n⚠️  Could not set target file %q: %v", target, err)
				return response
			}

			code := strings.TrimPrefix(response, "🔧 ")
			code = strings.TrimSpace(code)
			if code == "" {
				return response
			}

			msg, err := applyCodeToFile(target, code)
			if err != nil {
				response += fmt.Sprintf("\n⚠️  Could not apply to %s: %v", target, err)
			} else {
				response += fmt.Sprintf("\n✅ Applied to %s: %s", target, msg)
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

		// Handle slash commands.
		if handled, resp := handleCommand(line, mgr); handled {
			fmt.Println(resp)
			continue
		}

		fmt.Println(respond(line))
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("read stdin: %v", err)
	}
}
