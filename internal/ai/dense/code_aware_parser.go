package dense

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

var receiverRegex = regexp.MustCompile(`\(([^)]+)\)`)

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

	// 1. Extract explicit file target like "to file jim/jim.go" or "in file.go"
	fileTargetRegex := regexp.MustCompile(`(?i)(?:in|to|from)\s+(?:file\s+)?([a-zA-Z0-9_\-/\.]+\.go)`)
	if m := fileTargetRegex.FindStringSubmatch(prompt); len(m) > 1 {
		slot.ExplicitFile = filepath.Clean(m[1])
		prompt = fileTargetRegex.ReplaceAllString(prompt, "")
	}

	// 1b. Extract add-function intent like "add function jimmy"
	funcDeclRegex := regexp.MustCompile(`(?i)(?:add|create|make)\s+(?:func|function)\s+([a-zA-Z0-9_]+)`)
	if m := funcDeclRegex.FindStringSubmatch(prompt); len(m) > 1 {
		slot.Action = "ADD_FUNC"
		slot.FunctionName = strings.Title(m[1])
		slot.PayloadCode = slot.FunctionName + "() {\n\treturn\n}"
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

	// 3. Run the standard slot parser
	slot.ParsedSlot = ParsePromptWithSlots(prompt, graph)

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
