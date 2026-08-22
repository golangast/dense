package dense_test

import (
	"strings"
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

func TestParsePromptWithSlots_FuzzyAndSynsets(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Symbols: map[string]*dense.SymbolRef{
			"ProcessOrder": {Name: "ProcessOrder", FilePath: "order.go"},
			"User":         {Name: "User", FilePath: "user.go"},
		},
	}

	prompt1 := "please swap function ProcesOrdr for ProcessOrderV2() error { return nil }"
	res1 := dense.ParsePromptWithSlots(prompt1, graph)
	if res1.TargetSymbol != "ProcessOrder" {
		t.Fatalf("expected target symbol 'ProcessOrder', got %q", res1.TargetSymbol)
	}
	if res1.Action != "REPLACE" {
		t.Fatalf("expected action 'REPLACE', got %q", res1.Action)
	}
	if !strings.Contains(res1.PayloadCode, "ProcessOrderV2()") {
		t.Fatalf("expected payload code to include replacement signature, got %q", res1.PayloadCode)
	}

	prompt2 := "annotate struct Usr model with json tags"
	res2 := dense.ParsePromptWithSlots(prompt2, graph)
	if res2.TargetSymbol != "User" {
		t.Fatalf("expected target symbol 'User', got %q", res2.TargetSymbol)
	}
	if res2.Action != "INJECT_TAGS" {
		t.Fatalf("expected action 'INJECT_TAGS', got %q", res2.Action)
	}
}
