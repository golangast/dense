package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

func TestPredictIntentAndInjectIntentIntoAST_EvalHarness(t *testing.T) {
	model, err := dense.LoadGob("../../../data/models/dense/model.gob")
	if err != nil {
		// model may be absent in unit-test environments; keep the evaluation harness
		// runnable by using a minimal deterministic model seed instead.
		model = dense.NewDenseModel(len(dense.CommandVocab), []int{8}, len(dense.CommandLabels))
	}

	prompts := []string{
		"create function ComputeSum(a int, b int) int",
		"create function ValidateUser(name string) bool",
		"create method DoThing on Service(a int) error",
		"add import \"fmt\"",
		"add import \"context\"",
		"add unit test for DoWork",
		"create struct User",
		"create type Result struct",
		"add error check for err",
		"return empty string on error",
		"create function Process(items []string, m map[string]int) error",
		"create function FetchRates() ([]float64, error)",
		"create function CalculateTax(total float64, rates []float64) float64",
		"create function BuildURL(base string, path string) string",
		"create function ParseJSON(data []byte) (map[string]any, error)",
		"edit function response",
		"delete function cleanup",
		"add missing opening brace to function header",
		"add opening brace to if condition",
		"add opening brace to struct definition",
		"add opening brace to for loop",
		"add opening brace to switch statement",
		"hello how are you",
		"what can you do",
		"thank you for your help",
		"create file main.go",
		"modify file config.json",
		"delete file temp.txt",
		"create folder internal",
		"remove directory tmp",
		"list directory src",
		"show folder app",
		"create function LoadConfig(path string) (*Config, error)",
		"create function SendEmail(ctx context.Context, to string) error",
		"create function NormalizeName(s string) string",
		"create function ParseUserID(id string) (int64, error)",
		"create function GetUser(name string, age int) (User, error)",
		"create function Retry(fn func() error) error",
		"create function HashPassword(password string) ([]byte, error)",
		"create function BuildQuery(base string) string",
		"create function SaveJSON(v any) error",
		"create function CloneMap(src map[string]string) map[string]string",
		"create function RenderRow(row []string) string",
		"create function WithContext(ctx context.Context, fn func() error) error",
		"create function ReadFile(path string) ([]byte, error)",
		"create function RetryableCall(ctx context.Context, key string) (string, error)",
		"create function WrapError(err error) error",
		"create function Notify(ctx context.Context, msg string)",
		"create function Sum(a int, b int) int",
		"create function IsZero(i int) bool",
		"create function AddImport(path string) error",
		"create function MustParse(id string) int",
		"create function LastIndex(items []string) int",
		"create function CopyBytes(src []byte) []byte",
		"create function ValidateConfig(cfg Config) error",
	}

	if len(prompts) < 50 {
		t.Fatalf("expected at least 50 prompts, got %d", len(prompts))
	}

	for i, prompt := range prompts {
		intent := predictIntent(prompt, nil, model, nil)
		if intent.Action == "" {
			t.Fatalf("prompt %d produced empty intent: %q", i, prompt)
		}

		code, err := renderIntentToCode(intent)
		if err != nil {
			t.Fatalf("render intent for prompt %q: %v", prompt, err)
		}
		if code == "" {
			t.Fatalf("empty rendered code for prompt %q", prompt)
		}

		fileDir := t.TempDir()
		filePath := filepath.Join(fileDir, "sample.go")
		if err := os.WriteFile(filePath, []byte("package demo\n\n"), 0644); err != nil {
			t.Fatalf("write sample file: %v", err)
		}

		if intent.Receiver != "" {
			base := "package demo\n\ntype " + intent.Receiver + " struct{}\n\n"
			if err := os.WriteFile(filePath, []byte(base), 0644); err != nil {
				t.Fatalf("write receiver type: %v", err)
			}
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read file before injection: %v", err)
		}
		if _, _, err := applyCodeViaAST(filePath, string(content), code); err != nil {
			t.Fatalf("inject intent for prompt %q: %v", prompt, err)
		}

		content, err = os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read file after injection: %v", err)
		}
		if intent.Action == "ADD_IMPORT" {
			continue
		}
		if err := validateGoASTFile(filePath, string(content)); err != nil {
			t.Fatalf("typecheck failed for prompt %q: %v\ncontent:\n%s", prompt, err, content)
		}
	}
}
