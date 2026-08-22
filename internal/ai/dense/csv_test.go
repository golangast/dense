package dense

import "testing"

func TestNormalizePromptAndBalanceClassDistribution(t *testing.T) {
	examples := []CommandExample{
		{Type: "code_update", Prompt: " Add function Foo()  "},
		{Type: "code_update", Prompt: "add function foo()"},
		{Type: "code_update", Prompt: "add function Bar()"},
		{Type: "social", Prompt: "hello there"},
		{Type: "social", Prompt: "hello there!"},
		{Type: "file_create", Prompt: "create file main.go"},
		{Type: "file_create", Prompt: "create file util.go"},
		{Type: "file_create", Prompt: "create file other.go"},
	}

	unique := DeduplicateCommandExamples(examples)
	if got, want := len(unique), 6; got != want {
		t.Fatalf("DeduplicateCommandExamples() length = %d, want %d", got, want)
	}

	balanced := BalanceClassDistribution(unique, 2)
	if got, want := len(balanced), 5; got != want {
		t.Fatalf("BalanceClassDistribution() length = %d, want %d", got, want)
	}

	if got := NormalizePrompt("  Add   Function   Foo()  "); got != "add function foo" {
		t.Fatalf("NormalizePrompt() = %q, want %q", got, "add function foo")
	}
}
