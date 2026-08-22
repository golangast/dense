package dense_test

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

func TestHybridReplaceFunction(t *testing.T) {
	src := `package main

func jim() {
	println("old")
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "jim.go", src, 0)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	prompt := "replace jim with sally() int {return 3}"
	ok := dense.RouteAndExecute(file, prompt)
	if !ok {
		t.Fatalf("Expected successful AST mutation")
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatalf("Format error: %v", err)
	}

	expected := "func sally() int {\n\treturn 3\n}"
	if !bytes.Contains(buf.Bytes(), []byte("sally() int")) {
		t.Errorf("Expected output to contain %q, got:\n%s", expected, buf.String())
	}
}

func TestRouteAndExecuteWorkspaceResolvesBySymbol(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/workspace\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	filePath := filepath.Join(root, "sample.go")
	src := `package sample

func Hello() int {
	return 42
}`
	if err := os.WriteFile(filePath, []byte(src), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	graph, err := dense.IndexWorkspace(root)
	if err != nil {
		t.Fatalf("index workspace: %v", err)
	}

	resolved, ok := dense.RouteAndExecuteWorkspace(graph, "", "replace Hello with World() int { return 7 }")
	if !ok {
		t.Fatalf("expected workspace resolution and mutation to succeed")
	}
	if resolved != filePath {
		t.Fatalf("resolved path = %q, want %q", resolved, filePath)
	}

	astFile, ok := graph.Files[filePath]
	if !ok {
		t.Fatalf("workspace graph missing file %q", filePath)
	}
	if got := astFile.Decls[0].(*ast.FuncDecl).Name.Name; got != "World" {
		t.Fatalf("decl name = %q, want %q", got, "World")
	}
}

func TestColloquialSwapExtraction(t *testing.T) {
	prompt := "please swap function jim for sally() int { return 10 }"
	parsed := dense.ParseHybridPrompt(prompt)

	if len(parsed.Identifiers) == 0 || parsed.Identifiers[0] != "jim" {
		t.Fatalf("Expected identifier 'jim', got: %v", parsed.Identifiers)
	}

	if parsed.RawCode != "sally() int { return 10 }" {
		t.Fatalf("Expected raw code starting at 'sally', got: %q", parsed.RawCode)
	}
}
