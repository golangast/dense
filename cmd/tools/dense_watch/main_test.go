package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golangast/dense/internal/generator"
)

// copyTree copies files from src to dst, skipping .git, vendor and node_modules.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		// skip .git and large vendor folders
		if rel == ".git" || rel == "vendor" || rel == "node_modules" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		// copy file
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			return err
		}
		return nil
	})
}

func findRepoRoot(t *testing.T, start string) string {
	cur := start
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			t.Fatal("could not find repo root (go.mod)")
		}
		cur = parent
	}
}

func TestRunSuggestionsOneShot(t *testing.T) {
	if os.Getenv("DENSE_WATCH_TEST_NESTED") == "1" {
		t.Skip("skipping nested test execution to prevent infinite loop")
	}
	os.Setenv("DENSE_WATCH_TEST_NESTED", "1")
	defer os.Unsetenv("DENSE_WATCH_TEST_NESTED")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := findRepoRoot(t, wd)

	tmp, err := os.MkdirTemp("", "dense_suggest_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	if err := copyTree(repo, tmp); err != nil {
		t.Fatal(err)
	}

	// non-interactive: force auto-apply behavior
	globalAutoApply = true
	globalAutoRestoreGit = false

	// run the suggestion pass once on the temporary copy (short timeout)
	runSuggestions(tmp, 5*time.Second)

	// verify no remaining diagnostics
	diags, derr := generator.DiagnoseProject(tmp)
	if derr != nil {
		t.Fatalf("diagnose error: %v", derr)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics after suggestions, got: %v", diags)
	}
}
