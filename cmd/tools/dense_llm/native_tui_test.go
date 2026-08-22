package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffPreviewShowsChanges(t *testing.T) {
	before := "type User struct {\n    Name string\n}\n"
	after := "type User struct {\n    Name string `json:\"name\"`\n}\n"
	got := diffPreview(before, after)
	if !strings.Contains(got, "-     Name string") {
		t.Fatalf("diffPreview missing removal line: %q", got)
	}
	if !strings.Contains(got, "+     Name string `json:\"name\"`") {
		t.Fatalf("diffPreview missing addition line: %q", got)
	}
}

func TestSuggestionBarContainsHints(t *testing.T) {
	got := suggestionBar([]string{"add json tags", "wrap errors", "generate constructor"})
	if !strings.Contains(got, "add json tags") {
		t.Fatalf("suggestionBar missing first suggestion: %q", got)
	}
	if !strings.Contains(got, "generate constructor") {
		t.Fatalf("suggestionBar missing third suggestion: %q", got)
	}
}

func TestNativeTUIApplyPreviewWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("package main\n\ntype User struct {\n    Name string\n}\n"), 0644); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	msg, err := nativeTUIApplyPreview(path, "func Added() string { return \"ok\" }\n")
	if err != nil {
		t.Fatalf("nativeTUIApplyPreview returned error: %v", err)
	}
	if !strings.Contains(msg, path) {
		t.Fatalf("apply message missing file path: %q", msg)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if !strings.Contains(string(content), "func Added() string") {
		t.Fatalf("updated file missing generated function: %s", string(content))
	}
}
