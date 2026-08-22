package dense_test

import (
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

func TestPredictIntentNLP(t *testing.T) {
	prompt := "please swap function jim for sally() int { return 3 }"
	intent, score := dense.PredictIntentNLP(prompt)

	if intent != dense.IntentReplaceFunc {
		t.Errorf("Expected intent %s, got %s", dense.IntentReplaceFunc, intent)
	}

	if score <= 0.0 {
		t.Errorf("Expected positive confidence score, got %f", score)
	}
}

func TestParsePromptSlots_IgnoresPayloadNameAsTarget(t *testing.T) {
	prompt := "please swap function ProcessOrder for ProcessOrderV2() error { return nil }"
	slots := dense.ParsePromptSlots(prompt)

	if slots.Action != "REPLACE" {
		t.Fatalf("expected action REPLACE, got %q", slots.Action)
	}
	if slots.Target != "ProcessOrder" {
		t.Fatalf("expected target ProcessOrder, got %q", slots.Target)
	}
	if slots.Payload != "ProcessOrderV2() error { return nil }" {
		t.Fatalf("expected payload to capture replacement code, got %q", slots.Payload)
	}
}

func TestResolvePromptTarget_FuzzyMatchesWorkspaceSymbol(t *testing.T) {
	graph := &dense.WorkspaceGraph{Symbols: map[string]*dense.SymbolRef{
		"User": {Name: "User", Kind: "struct", Package: "example.com/model", FilePath: "/tmp/user.go"},
	}}

	resolved, found := dense.ResolvePromptTarget(graph, "add json tags to user model")
	if !found {
		t.Fatal("expected fuzzy target resolution to find User")
	}
	if resolved != "User" {
		t.Fatalf("expected resolved symbol User, got %q", resolved)
	}
}
