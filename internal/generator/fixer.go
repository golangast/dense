package generator

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// GoError describes a single compiler or test error recognized from Go output.
type GoError struct {
	FilePath string
	Line     int
	Column   int
	Message  string
}

var stdPkgMap = map[string]string{
	"fmt":      "fmt",
	"http":     "net/http",
	"json":     "encoding/json",
	"os":       "os",
	"time":     "time",
	"bytes":    "bytes",
	"strings":  "strings",
	"context":  "context",
	"sync":     "sync",
	"filepath": "path/filepath",
	"io":       "io",
	"exec":     "os/exec",
	"regexp":   "regexp",
	"ast":      "go/ast",
	"token":    "go/token",
	"parser":   "go/parser",
}

// StdPkgMap exposes the internal stdPkgMap for external preview helpers.
func StdPkgMap() map[string]string {
	return stdPkgMap
}

// DiagnoseProject runs the Go toolchain against a project and captures compiler errors.
func DiagnoseProject(dir string) ([]GoError, error) {
	if dir == "" {
		dir = "."
	}

	out, _ := runGoCommand(dir, "go", "test", "./...")
	if parsed := parseGoErrors(out); len(parsed) > 0 {
		return parsed, nil
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	if len(files) == 0 {
		return nil, nil
	}
	var all []GoError
	for _, file := range files {
		out, _ = runGoCommand(dir, "go", "run", file)
		if parsed := parseGoErrors(out); len(parsed) > 0 {
			all = append(all, parsed...)
		}
	}
	if len(all) > 0 {
		return all, nil
	}
	return nil, nil
}

func runGoCommand(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func parseGoErrors(output string) []GoError {
	re := regexp.MustCompile(`(?m)^(.+?\.go):(\d+):(\d+):\s+(.+)$`)
	matches := re.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]GoError, 0, len(matches))
	for _, m := range matches {
		if len(m) < 5 {
			continue
		}
		line, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		out = append(out, GoError{
			FilePath: m[1],
			Line:     line,
			Column:   col,
			Message:  m[4],
		})
	}
	return out
}

// AutoFixFile attempts to repair common go compilation errors using AST-editing.
func AutoFixFile(errItem GoError) bool {
	if strings.TrimSpace(errItem.FilePath) == "" {
		return false
	}
	// Try parsing the file; if parse fails due to syntax errors, we still
	// attempt some conservative, local fixes based on the compiler message.
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, errItem.FilePath, nil, parser.ParseComments)
	if err != nil {
		// handle some common syntax errors heuristically
		// e.g., "non-declaration statement outside function body" -> comment out the line
		if strings.Contains(errItem.Message, "non-declaration statement outside function body") {
			// read file and try to repair a common case: a function signature
			// missing its opening brace on the previous line. If detected,
			// insert the '{' after the signature. Otherwise fall back to
			// commenting the offending line.
			data, rerr := os.ReadFile(errItem.FilePath)
			if rerr != nil {
				return false
			}
			lines := strings.Split(string(data), "\n")
			ln := errItem.Line - 1
			prev := ln - 1
			fixed := false
			if prev >= 0 && prev < len(lines) {
				trim := strings.TrimSpace(lines[prev])
				// detect a probable func signature line
				if strings.HasPrefix(trim, "func ") && strings.HasSuffix(trim, ")") && !strings.Contains(lines[prev], "{") {
					// Inspect a few lines after the signature to guess a return type.
					retType := ""
					for i := ln; i < len(lines) && i < ln+8; i++ {
						s := strings.TrimSpace(lines[i])
						if strings.HasPrefix(s, "return ") {
							if strings.Contains(s, "fmt.Sprintf") || strings.Contains(s, "\"") {
								retType = "string"
								break
							}
							// other heuristics could go here
						}
					}
					if retType != "" {
						// replace trailing ')' with ') <retType> {'
						if idx := strings.LastIndex(lines[prev], ")"); idx != -1 {
							lines[prev] = lines[prev][:idx+1] + " " + retType + " {"
						} else {
							lines[prev] = lines[prev] + " " + retType + " {"
						}
					} else {
						lines[prev] = lines[prev] + " {"
					}
					fixed = true
				}
			}
			if !fixed && ln >= 0 && ln < len(lines) {
				if !strings.HasPrefix(strings.TrimSpace(lines[ln]), "//") {
					lines[ln] = "// " + lines[ln]
					fixed = true
				}
			}
			if fixed {
				out := strings.Join(lines, "\n")
				if werr := os.WriteFile(errItem.FilePath, []byte(out), 0644); werr == nil {
					return true
				}
			}
		}
		// handle missing '{' after a struct/type line: e.g. "expected '{', found Addr"
		if strings.Contains(errItem.Message, "expected '{', found") || strings.Contains(errItem.Message, "expected {") {
			data, rerr := os.ReadFile(errItem.FilePath)
			if rerr != nil {
				return false
			}
			lines := strings.Split(string(data), "\n")
			ln := errItem.Line - 1
			prev := ln - 1
			if prev >= 0 && prev < len(lines) {
				trim := strings.TrimSpace(lines[prev])
				// detect a probable 'type X struct' line lacking an opening brace
				if strings.HasPrefix(trim, "type ") && strings.Contains(trim, "struct") && !strings.Contains(lines[prev], "{") {
					lines[prev] = lines[prev] + " {"
					out := strings.Join(lines, "\n")
					if werr := os.WriteFile(errItem.FilePath, []byte(out), 0644); werr == nil {
						return true
					}
				}
			}
		}

		// handle composite-literal newline errors by adding a trailing comma
		if strings.Contains(errItem.Message, "unexpected newline in composite literal") || strings.Contains(errItem.Message, "possibly missing comma") {
			data, rerr := os.ReadFile(errItem.FilePath)
			if rerr != nil {
				return false
			}
			lines := strings.Split(string(data), "\n")
			ln := errItem.Line - 1
			prev := ln - 1
			if prev >= 0 && prev < len(lines) {
				if !strings.HasSuffix(strings.TrimRight(lines[prev], " \t"), ",") && !strings.HasSuffix(strings.TrimRight(lines[prev], " \t"), "}") {
					lines[prev] = lines[prev] + ","
					out := strings.Join(lines, "\n")
					if werr := os.WriteFile(errItem.FilePath, []byte(out), 0644); werr == nil {
						return true
					}
				}
			}
		}

		// handle split 'err !=' style broken lines: join with following 'nil' line
		if strings.Contains(errItem.Message, "unexpected name error at end of statement") || strings.Contains(errItem.Message, "expected '{' after function body") {
			data, rerr := os.ReadFile(errItem.FilePath)
			if rerr != nil {
				return false
			}
			lines := strings.Split(string(data), "\n")
			ln := errItem.Line - 1
			if ln >= 1 && ln+1 < len(lines) {
				left := strings.TrimRight(lines[ln-1], " \t")
				right := strings.TrimSpace(lines[ln])
				if strings.HasSuffix(left, "!=") && (strings.HasPrefix(right, "nil") || strings.HasPrefix(right, "err")) {
					lines[ln-1] = left + " " + right
					// remove the now-merged line
					lines = append(lines[:ln], lines[ln+1:]...)
					out := strings.Join(lines, "\n")
					if werr := os.WriteFile(errItem.FilePath, []byte(out), 0644); werr == nil {
						return true
					}
				}
			}
		}
		return false
	}
	if file.Name == nil {
		file.Name = ast.NewIdent("main")
	}
	fixed := false
	for _, match := range regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(errItem.Message, -1) {
		if len(match) < 2 {
			continue
		}
		ident := match[1]
		if pkgPath, exists := stdPkgMap[ident]; exists {
			if AddImportToAST(file, pkgPath) {
				fixed = true
			}
		}
	}
	if !fixed {
		// Conservative heuristic: if the undefined identifier appears as a lone
		// struct field (missing a type) in this file, append a `string` type.
		for _, match := range regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(errItem.Message, -1) {
			if len(match) < 2 {
				continue
			}
			ident := match[1]
			// read raw file and search for a field line that is exactly the ident
			data, rerr := os.ReadFile(errItem.FilePath)
			if rerr != nil {
				continue
			}
			lines := strings.Split(string(data), "\n")
			modified := false
			for i, L := range lines {
				if strings.TrimSpace(L) == ident {
					// check previous non-empty line for 'struct' indicator
					prev := i - 1
					for prev >= 0 && strings.TrimSpace(lines[prev]) == "" {
						prev--
					}
					if prev >= 0 && strings.Contains(lines[prev], "struct") {
						lines[i] = lines[i] + " string"
						modified = true
						break
					}
				}
			}
			if modified {
				out := strings.Join(lines, "\n")
				if werr := os.WriteFile(errItem.FilePath, []byte(out), 0644); werr == nil {
					return true
				}
			}
		}
		return false
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, token.NewFileSet(), file); err != nil {
		return false
	}
	if err := os.WriteFile(errItem.FilePath, buf.Bytes(), 0644); err != nil {
		return false
	}
	return true
}

// AddImportToAST dynamically inserts a missing import path into the AST file header.
func AddImportToAST(file *ast.File, importPath string) bool {
	if file == nil || strings.TrimSpace(importPath) == "" {
		return false
	}
	for _, imp := range file.Imports {
		if imp != nil && imp.Path != nil && imp.Path.Value == strconv.Quote(importPath) {
			return false
		}
	}
	newImport := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(importPath)}}
	for _, decl := range file.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok && genDecl.Tok == token.IMPORT {
			genDecl.Specs = append(genDecl.Specs, newImport)
			return true
		}
	}
	importDecl := &ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{newImport}}
	file.Decls = append([]ast.Decl{importDecl}, file.Decls...)
	return true
}

// TryAutoFixFile attempts a minimal safe fix for a broken Go file based on the compiler error.
func TryAutoFixFile(filePath, message string) (bool, error) {
	if strings.TrimSpace(filePath) == "" {
		return false, fmt.Errorf("empty file path")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false, err
	}
	file, parseErr := parser.ParseFile(token.NewFileSet(), filePath, content, parser.ParseComments)
	if parseErr != nil {
		return false, parseErr
	}
	fixed := false
	for _, match := range regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(message, -1) {
		if len(match) < 2 {
			continue
		}
		if pkgPath, exists := stdPkgMap[match[1]]; exists {
			if AddImportToAST(file, pkgPath) {
				fixed = true
			}
		}
	}
	if !fixed {
		return false, nil
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, token.NewFileSet(), file); err != nil {
		return false, err
	}
	if err := os.WriteFile(filePath, buf.Bytes(), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// ExampleIntentCommand is a small helper used to route generated commands in tests.
func ExampleIntentCommand(prompt string) string {
	switch {
	case len(prompt) == 0:
		return ""
	case regexp.MustCompile(`(?i)\bfix\b|\bcompilation\b|\berror\b`).MatchString(prompt):
		return "cmd_fix"
	case regexp.MustCompile(`(?i)\bgenerate\b|\bhandler\b|\bscaffold\b`).MatchString(prompt):
		return "cmd_generate"
	case regexp.MustCompile(`(?i)\brestful\b|\bapi\b|\bhttp\b`).MatchString(prompt):
		return "cmd_scaffold"
	default:
		return "cmd_generate"
	}
}

var _ = fmt.Sprintf
