package main

import (
	"os"
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

// reuses findRepoRoot from main_test.go

func TestApplyReplaceNewServer(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := findRepoRoot(t, wd)

	// index workspace in-place (no copy) but do not write any files
	graph, err := dense.IndexWorkspace(repo)
	if err != nil {
		t.Fatalf("index error: %v", err)
	}

	// locate NewServer symbol
	sym, ok := graph.FindSymbol("NewServer")
	if !ok {
		t.Skip("NewServer not found in workspace; skipping")
	}

	fileAST := graph.Files[sym.FilePath]
	if fileAST == nil {
		t.Fatalf("file AST not loaded for %s", sym.FilePath)
	}

	// perform a non-destructive replace in-memory
	newCode := "func NewServer(addr string) *Server { return &Server{Addr: addr} }"
	if !dense.ReplaceFunctionDecl(fileAST, "NewServer", newCode) {
		t.Fatalf("ReplaceFunctionDecl failed for %s", sym.FilePath)
	}

	// ReplaceFunctionDecl returned true on success; that's our assertion.
}
