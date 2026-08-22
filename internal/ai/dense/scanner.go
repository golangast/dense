package dense

import (
	"go/ast"
	"go/scanner"
	"go/token"
	"strings"
)

// ─── Lexical Tokenization ─────────────────────────────────────────────────────

// ParsedPrompt holds the structured result of scanning a natural-language prompt
// with Go's lexical scanner.
type ScannerParsedPrompt struct {
	// Action is the first verb found (e.g. "import", "replace", "add", "remove", "wrap").
	Action string
	// Identifiers are the Go-like identifiers found after the action verb.
	Identifiers []string
	// FilePaths holds any tokens that look like file paths (contain "/" or end in ".go").
	FilePaths []string
	// CodeLiteral contains any raw code block captured between braces `{ ... }`.
	CodeLiteral string
	// RawTokens is the flat list of all non-EOF tokens for downstream use.
	RawTokens []string
}

// intentVerbs is the set of words that are treated as action verbs.
var intentVerbs = map[string]bool{
	"import":  true,
	"replace": true,
	"add":     true,
	"remove":  true,
	"delete":  true,
	"wrap":    true,
	"create":  true,
	"rename":  true,
	"move":    true,
	"use":     true,
}

// ScanPrompt tokenizes the NL prompt using Go's go/scanner so that Go identifiers,
// operators, and quoted strings are parsed consistently with the language spec —
// not arbitrary word-splitting. This gives downstream classifiers clean verb /
// identifier signals.
func ScanPrompt(prompt string) ScannerParsedPrompt {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(prompt))
	// Skip error handler — NL text is not valid Go, errors are expected.
	s.Init(file, []byte(prompt), nil, scanner.ScanComments)

	var res ScannerParsedPrompt
	braceDepth := 0
	var braceBuilder strings.Builder

	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}

		// Capture raw code blocks between braces.
		switch tok {
		case token.LBRACE:
			braceDepth++
			braceBuilder.WriteString("{")
			continue
		case token.RBRACE:
			braceDepth--
			braceBuilder.WriteString("}")
			if braceDepth == 0 && braceBuilder.Len() > 2 {
				res.CodeLiteral = braceBuilder.String()
				braceBuilder.Reset()
			}
			continue
		}
		if braceDepth > 0 {
			if lit != "" {
				braceBuilder.WriteString(lit)
			} else {
				braceBuilder.WriteString(tok.String())
			}
			braceBuilder.WriteString(" ")
			continue
		}

		// Collect the textual form of the token.
		text := lit
		if text == "" {
			text = tok.String()
		}

		res.RawTokens = append(res.RawTokens, text)

		switch tok {
		case token.IDENT:
			lower := strings.ToLower(text)
			if res.Action == "" && intentVerbs[lower] {
				res.Action = lower
			} else {
				// Skip filler words.
				if !isFillerWord(lower) {
					res.Identifiers = append(res.Identifiers, text)
				}
			}
		case token.QUO: // "/" — part of a file path like jim/jake.go
			// handled by merging adjacent ident/slash/ident below via RawTokens pass
		case token.STRING:
			// Quoted string — strip quotes and treat as a path or identifier.
			stripped := strings.Trim(text, `"'`+"`")
			if stripped != "" {
				if strings.Contains(stripped, "/") || strings.HasSuffix(stripped, ".go") {
					res.FilePaths = append(res.FilePaths, stripped)
				} else {
					res.Identifiers = append(res.Identifiers, stripped)
				}
			}
		}
	}

	// Second pass: reassemble path-like token sequences (e.g. "jim / jake . go").
	res.FilePaths = append(res.FilePaths, extractFilePaths(res.RawTokens)...)

	return res
}

// extractFilePaths scans the raw token list for sequences that look like file
// system paths (tokens separated by "/" with a trailing ".go").
func extractFilePaths(tokens []string) []string {
	var paths []string
	n := len(tokens)
	i := 0
	for i < n {
		// Look for an identifier followed by "/" separated segments ending in ".go"
		if i+2 < n && tokens[i+1] == "/" {
			var sb strings.Builder
			sb.WriteString(tokens[i])
			j := i + 1
			for j+1 < n && tokens[j] == "/" {
				sb.WriteString("/")
				sb.WriteString(tokens[j+1])
				j += 2
			}
			candidate := sb.String()
			if strings.HasSuffix(candidate, ".go") || strings.Contains(candidate, "/") {
				paths = append(paths, candidate)
				i = j
				continue
			}
		}
		i++
	}
	return paths
}

// isFillerWord returns true for English stopwords that carry no semantic weight
// for intent classification.
func isFillerWord(s string) bool {
	switch s {
	case "the", "a", "an", "and", "or", "in", "into", "from", "to",
		"of", "with", "for", "on", "at", "by", "is", "it", "its",
		"as", "be", "that", "this", "struct", "function", "file", "func",
		"type", "package", "method", "use":
		return true
	}
	return false
}

// ─── Hybrid Feature Vector ────────────────────────────────────────────────────

// HybridFeatureSize is the total dimension of the hybrid feature vector.
const HybridFeatureSize = 64

// ExtractHybridFeatures combines prompt text tokens (slots 0–15) with AST context
// metadata about the target file (slots 16–31) into a fixed-size float32 vector
// suitable for feeding into the dense classifier.
//
// Slot layout:
//
//	0:  action == "replace"
//	1:  action == "add" / "import"
//	2:  action == "remove" / "delete"
//	3:  action == "wrap"
//	4:  action == "create"
//	5:  action == "rename"
//	6:  has code literal (brace-delimited block)
//	7:  has quoted import path
//	8:  has file path (.go file mentioned)
//	9:  identifier count > 1
//	10: identifier count > 3
//	11: "json" or "tag" in identifiers
//	12: "error" or "err" in identifiers
//	13: "test" in identifiers
//	14: "struct" in raw tokens
//	15: "func"/"function" in raw tokens
//	16: AST — named identifier exists as FuncDecl in target file
//	17: AST — named identifier exists as TypeSpec (struct/type) in target file
//	18: AST — named identifier exists as import in target file
//	19: AST — function has pointer receiver
//	20: AST — function returns error
//	21: AST — file has any struct declarations
//	22: AST — file has any interface declarations
//	23: AST — file already imports "fmt"
//	24–63: reserved / zero
func ExtractHybridFeatures(parsed ScannerParsedPrompt, file *ast.File) []float32 {
	vec := make([]float32, HybridFeatureSize)

	// ── Slots 0–15: Prompt text features ──────────────────────────────────────
	switch parsed.Action {
	case "replace":
		vec[0] = 1
	case "add", "import", "use":
		vec[1] = 1
	case "remove", "delete":
		vec[2] = 1
	case "wrap":
		vec[3] = 1
	case "create":
		vec[4] = 1
	case "rename", "move":
		vec[5] = 1
	}

	if parsed.CodeLiteral != "" {
		vec[6] = 1
	}
	if len(parsed.FilePaths) > 0 {
		vec[7] = 1
	}
	// file path ending in .go
	for _, p := range parsed.FilePaths {
		if strings.HasSuffix(p, ".go") {
			vec[8] = 1
			break
		}
	}
	if len(parsed.Identifiers) > 1 {
		vec[9] = 1
	}
	if len(parsed.Identifiers) > 3 {
		vec[10] = 1
	}

	lowerIdents := make([]string, len(parsed.Identifiers))
	for i, id := range parsed.Identifiers {
		lowerIdents[i] = strings.ToLower(id)
	}
	rawLower := strings.ToLower(strings.Join(parsed.RawTokens, " "))

	if strings.Contains(rawLower, "json") || strings.Contains(rawLower, "tag") {
		vec[11] = 1
	}
	if strings.Contains(rawLower, "error") || strings.Contains(rawLower, "err") {
		vec[12] = 1
	}
	if strings.Contains(rawLower, "test") {
		vec[13] = 1
	}
	if strings.Contains(rawLower, "struct") {
		vec[14] = 1
	}
	if strings.Contains(rawLower, "func") || strings.Contains(rawLower, "function") {
		vec[15] = 1
	}

	// ── Slots 16–23: AST context features ─────────────────────────────────────
	if file != nil && len(parsed.Identifiers) > 0 {
		for _, ident := range parsed.Identifiers {
			if astDeclExists(file, ident, "func") {
				vec[16] = 1
			}
			if astDeclExists(file, ident, "type") {
				vec[17] = 1
			}
			if astImportExists(file, ident) {
				vec[18] = 1
			}
		}
		// Check first identifier against function signatures.
		if fn := findFuncDecl(file, parsed.Identifiers[0]); fn != nil {
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				if _, isPtr := fn.Recv.List[0].Type.(*ast.StarExpr); isPtr {
					vec[19] = 1
				}
			}
			if funcReturnsError(fn) {
				vec[20] = 1
			}
		}
	}
	if file != nil {
		hasStruct, hasIface := false, false
		ast.Inspect(file, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok && ts.Type != nil {
				switch ts.Type.(type) {
				case *ast.StructType:
					hasStruct = true
				case *ast.InterfaceType:
					hasIface = true
				}
			}
			return true
		})
		if hasStruct {
			vec[21] = 1
		}
		if hasIface {
			vec[22] = 1
		}
		if astImportExists(file, "fmt") {
			vec[23] = 1
		}
	}

	return vec
}

// ─── AST Lookup Helpers ───────────────────────────────────────────────────────

// astDeclExists returns true if an identifier with the given name and kind
// ("func" or "type") exists as a top-level declaration in the file.
func astDeclExists(file *ast.File, name, kind string) bool {
	for _, decl := range file.Decls {
		switch kind {
		case "func":
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == name {
				return true
			}
		case "type":
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, spec := range gd.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name != nil && ts.Name.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// astImportExists returns true if the file already imports a path containing name.
func astImportExists(file *ast.File, name string) bool {
	for _, imp := range file.Imports {
		if imp.Path != nil {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, name) {
				return true
			}
		}
	}
	return false
}

// findFuncDecl returns the *ast.FuncDecl for the given function name, or nil.
func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// funcReturnsError reports whether any result of fn is the built-in error type.
func funcReturnsError(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "error" {
			return true
		}
	}
	return false
}
