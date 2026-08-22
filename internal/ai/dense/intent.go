package dense

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

var stopWords = map[string]bool{
	"please": true, "swap": true, "replace": true, "function": true,
	"fn": true, "method": true, "for": true, "with": true, "to": true,
}

type ParsedPrompt struct {
	Action      string
	Identifiers []string
	RawCode     string
}

func ParseHybridPrompt(prompt string) ParsedPrompt {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(prompt))
	s.Init(file, []byte(prompt), nil, 0)

	var res ParsedPrompt
	var tokens []string
	var positions []token.Pos

	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		positions = append(positions, pos)
		if lit != "" {
			tokens = append(tokens, lit)
		} else {
			tokens = append(tokens, tok.String())
		}
	}

	for i, t := range tokens {
		lower := strings.ToLower(t)
		if lower == "replace" || lower == "swap" || lower == "change" || lower == "substitute" || lower == "update" {
			res.Action = "replace"
		}

		if (lower == "with" || lower == "for") && i+1 < len(positions) {
			codeStart := positions[i+1] - 1
			res.RawCode = strings.TrimSpace(prompt[codeStart:])
			for _, prefix := range []string{"for ", "with ", "to "} {
				if strings.HasPrefix(strings.ToLower(res.RawCode), prefix) {
					res.RawCode = strings.TrimSpace(res.RawCode[len(prefix):])
					break
				}
			}
			break
		}

		if t == "(" && i > 0 {
			codeStart := positions[i-1] - 1
			res.RawCode = strings.TrimSpace(prompt[codeStart:])
			break
		}

		if res.Action != "" && len(res.Identifiers) == 0 {
			if !stopWords[lower] && token.IsIdentifier(t) {
				res.Identifiers = append(res.Identifiers, t)
			}
		}
	}

	return res
}

func ReplaceFunctionDecl(file *ast.File, targetName string, newFuncSource string) bool {
	if file == nil || targetName == "" {
		return false
	}

	snippet := strings.TrimSpace(newFuncSource)
	if snippet == "" {
		return false
	}
	if !strings.HasPrefix(snippet, "func ") {
		snippet = "func " + snippet
	}

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "", "package dummy\n"+snippet, 0)
	if err != nil || len(parsed.Decls) == 0 {
		return false
	}

	newFuncDecl, ok := parsed.Decls[0].(*ast.FuncDecl)
	if !ok {
		return false
	}

	for i, decl := range file.Decls {
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name.Name == targetName {
			file.Decls[i] = newFuncDecl
			return true
		}
	}

	return false
}

func RouteAndExecute(file *ast.File, prompt string) bool {
	parsed := ParseHybridPrompt(prompt)

	switch parsed.Action {
	case "replace":
		if len(parsed.Identifiers) > 0 && parsed.RawCode != "" {
			target := parsed.Identifiers[0]
			newCode := parsed.RawCode
			if !strings.HasPrefix(newCode, "func ") {
				newCode = "func " + newCode
			}
			return ReplaceFunctionDecl(file, target, newCode)
		}
	case "add":
		if len(parsed.Identifiers) > 0 {
			return AutoInjectJSONTags(file, parsed.Identifiers[0])
		}
	case "wrap":
		if len(parsed.Identifiers) > 0 {
			return WrapErrorsInFunc(file, parsed.Identifiers[0])
		}
	}

	return false
}

func RouteAndExecuteWorkspace(graph *WorkspaceGraph, targetFile string, prompt string) (string, bool) {
	slot := ParsePromptWithSlots(prompt, graph)
	return RouteAndExecuteWorkspaceWithSlot(graph, targetFile, slot)
}

func RouteAndExecuteWorkspaceWithSlot(graph *WorkspaceGraph, targetFile string, slot ParsedSlot) (string, bool) {
	if graph == nil {
		return "", false
	}
	if slot.TargetSymbol == "" {
		return "", false
	}

	actualFile := targetFile
	if actualFile == "" {
		if sym, found := graph.Symbols[slot.TargetSymbol]; found {
			actualFile = sym.FilePath
		}
	}
	if actualFile == "" {
		return "", false
	}

	fileAST, ok := graph.Files[actualFile]
	if !ok {
		return actualFile, false
	}

	switch slot.Action {
	case "REPLACE":
		if slot.PayloadCode != "" {
			return actualFile, ReplaceFunctionDecl(fileAST, slot.TargetSymbol, slot.PayloadCode)
		}
	case "INJECT_TAGS":
		return actualFile, AutoInjectJSONTags(fileAST, slot.TargetSymbol)
	}

	return actualFile, false
}

// RouteAndExecuteWorkspaceWithCodeAwareSlot handles CodeAwareSlot specifically,
// allowing explicit file targets and ADD_FUNC actions.
func RouteAndExecuteWorkspaceWithCodeAwareSlot(graph *WorkspaceGraph, targetFile string, slot CodeAwareSlot) (string, bool) {
	if graph == nil {
		return "", false
	}

	actualFile := targetFile
	if actualFile == "" {
		if slot.ExplicitFile != "" {
			actualFile = slot.ExplicitFile
		} else if slot.TargetSymbol != "" {
			if sym, found := graph.Symbols[slot.TargetSymbol]; found {
				actualFile = sym.FilePath
			}
		}
	}

	if actualFile == "" {
		return "", false
	}

	fileAST, exists := graph.Files[actualFile]
	if !exists {
		return actualFile, false
	}

	switch slot.Action {
	case "ADD_FUNC":
		return actualFile, AppendFunctionDecl(fileAST, slot.PayloadCode)
	case "REPLACE":
		return actualFile, ReplaceFunctionDecl(fileAST, slot.TargetSymbol, slot.PayloadCode)
	case "INJECT_TAGS":
		return actualFile, AutoInjectJSONTags(fileAST, slot.TargetSymbol)
	}

	return actualFile, false
}

func RouteAndExecuteNLP(graph *WorkspaceGraph, targetFile string, prompt string) (string, bool) {
	if graph == nil {
		return "", false
	}

	predictedIntent, confidence := PredictIntentNLP(prompt)
	if confidence < 0.20 {
		return RouteAndExecuteWorkspace(graph, targetFile, prompt)
	}

	parsed := ParseHybridPrompt(prompt)
	actualFile := targetFile
	if actualFile == "" && len(parsed.Identifiers) > 0 {
		for _, ident := range parsed.Identifiers {
			if sym, found := graph.FindSymbol(ident); found {
				actualFile = sym.FilePath
				break
			}
		}
	}
	if actualFile == "" {
		return "", false
	}

	fileAST, ok := graph.Files[actualFile]
	if !ok {
		return actualFile, false
	}

	switch predictedIntent {
	case IntentReplaceFunc:
		if len(parsed.Identifiers) > 0 && parsed.RawCode != "" {
			return actualFile, ReplaceFunctionDecl(fileAST, parsed.Identifiers[0], parsed.RawCode)
		}
	case IntentAddTags:
		if len(parsed.Identifiers) > 0 {
			return actualFile, AutoInjectJSONTags(fileAST, parsed.Identifiers[0])
		}
	}

	return actualFile, false
}
