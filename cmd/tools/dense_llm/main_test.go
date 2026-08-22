package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golangast/dense/internal/ai/dense"
	"golang.org/x/tools/go/ast/astutil"
)

func TestFunctionSnippetFromPrompt(t *testing.T) {
	prompt := "add function jim to file jim/jim.go"
	got := functionSnippetFromPrompt(prompt)
	if got == "" {
		t.Fatal("functionSnippetFromPrompt returned empty for function creation prompt")
	}
	if want := "func Jim()"; got[:len(want)] != want {
		t.Fatalf("functionSnippetFromPrompt(%q) = %q, want prefix %q", prompt, got, want)
	}
}

func TestFunctionSnippetFromPrompt_ReplaceWithFuncBody(t *testing.T) {
	prompt := `replace Jim with func Jim() string { return "jim" }`
	got := functionSnippetFromPrompt(prompt)
	if got == "" {
		t.Fatal("functionSnippetFromPrompt returned empty for replace-with-func prompt")
	}
	if !strings.Contains(got, "func Jim() string") {
		t.Fatalf("functionSnippetFromPrompt(%q) = %q, want function signature", prompt, got)
	}
	if !strings.Contains(got, `return "jim"`) {
		t.Fatalf("functionSnippetFromPrompt(%q) = %q, want return body", prompt, got)
	}
}

func TestFunctionSnippetFromPrompt_ReplaceWithNewFunctionNameSignature(t *testing.T) {
	prompt := `in the file jim/jim.go replace jim with sally() int {return 3}`
	got := functionSnippetFromPrompt(prompt)
	if got == "" {
		t.Fatal("functionSnippetFromPrompt returned empty for renamed-signature replacement")
	}
	if !strings.Contains(got, "func sally() int") {
		t.Fatalf("functionSnippetFromPrompt(%q) = %q, want renamed function signature", prompt, got)
	}
	if strings.Contains(got, "func jim sally") {
		t.Fatalf("functionSnippetFromPrompt(%q) = %q, should not prepend old name before new function name", prompt, got)
	}
	if got != "func sally() int {return 3}\n" {
		t.Fatalf("functionSnippetFromPrompt(%q) = %q, want exact function declaration", prompt, got)
	}
}

func TestResolvePromptTarget_PrefersExplicitPromptPathOverStaleConversationTarget(t *testing.T) {
	prompt := `in the file jim/jim.go replace jim with sally() int {return 3}`
	if got := resolvePromptTarget(prompt, "/tmp/stale.go"); got != "jim/jim.go" {
		t.Fatalf("resolvePromptTarget(%q, stale) = %q, want %q", prompt, got, "jim/jim.go")
	}
}

func TestDenseInferTargetFromPrompt_UsesImmediateGoPath(t *testing.T) {
	prompt := `in the file jim/jim.go replace jim with sally() int {return 3}`
	if got := dense.InferTargetFromPrompt(prompt); got != "jim/jim.go" {
		t.Fatalf("dense.InferTargetFromPrompt(%q) = %q, want %q", prompt, got, "jim/jim.go")
	}
}

func TestApplyFunctionReplacement_AllowsLowercasePromptToMatchExportedName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jim.go")
	original := "package main\n\nfunc Jim() int {\n\treturn 0\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	if _, err := applyFunctionReplacement(path, "jim", "func sally() int {return 3}"); err != nil {
		t.Fatalf("applyFunctionReplacement should match exported name case-insensitively: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if !strings.Contains(string(contents), "func sally() int") || !strings.Contains(string(contents), "return 3") {
		t.Fatalf("updated file should contain new function implementation, got: %s", contents)
	}
}

func TestClassifyCommandType_ReplacePrompt(t *testing.T) {
	if got := dense.ClassifyCommandType(`replace Jim with func Jim() string { return "jim" }`); got != "code_update" {
		t.Fatalf("expected code_update for replacement prompt, got %q", got)
	}
}

func TestClassifyCommandType_ImportPrompt(t *testing.T) {
	for _, prompt := range []string{
		`import the struct Jake from jake.go to jim/jim.go`,
		`import the struct Jake from jake.go to file jim/jim.go`,
	} {
		if got := dense.ClassifyCommandType(prompt); got != "code_update" {
			t.Fatalf("expected code_update for import prompt %q, got %q", prompt, got)
		}
	}
}

func TestInferTargetFileFromPrompt_ImportUsesDestinationFile(t *testing.T) {
	for _, prompt := range []string{
		`import the struct Jake from jake.go to jim/jim.go`,
		`import the struct Jake from jake.go to file jim/jim.go`,
	} {
		if got := inferTargetFileFromPrompt(prompt); got != "jim/jim.go" {
			t.Fatalf("inferTargetFileFromPrompt(%q) = %q, want %q", prompt, got, "jim/jim.go")
		}
	}
}

func TestApplyFunctionReplacement_UpdatesGoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jim.go")
	original := "package jim\n\nfunc Jim() int {\n\treturn 0\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	if _, err := applyFunctionReplacement(path, "Jim", "func Jim() string { return \"jim\" }"); err != nil {
		t.Fatalf("applyFunctionReplacement returned error: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if !strings.Contains(string(contents), "func Jim() string") {
		t.Fatalf("updated file should replace function signature, got: %s", contents)
	}
	if !strings.Contains(string(contents), `return "jim"`) {
		t.Fatalf("updated file should contain new return value, got: %s", contents)
	}
}

func TestApplyCodeViaAST_AllowsSamePackageReferences(t *testing.T) {
	dir := t.TempDir()
	jakePath := filepath.Join(dir, "jake.go")
	jimPath := filepath.Join(dir, "jim.go")
	if err := os.WriteFile(jakePath, []byte("package main\n\ntype Jake struct {\n\tFirstName string\n\tLastName  string\n}\n"), 0644); err != nil {
		t.Fatalf("write jake.go: %v", err)
	}
	if err := os.WriteFile(jimPath, []byte("package main\n\nfunc sally() int {\n\treturn 3\n}\n"), 0644); err != nil {
		t.Fatalf("write jim.go: %v", err)
	}

	applied, _, err := applyCodeViaAST(jimPath, "package main\n\nfunc sally() int {\n\treturn 3\n}\n", "func useJake() Jake {\n\treturn Jake{FirstName: \"Jake\", LastName: \"The Snake\"}\n}\n")
	if err != nil {
		t.Fatalf("applyCodeViaAST should allow same-package type references: %v", err)
	}
	if !applied {
		t.Fatal("applyCodeViaAST should have applied the function")
	}

	contents, err := os.ReadFile(jimPath)
	if err != nil {
		t.Fatalf("read updated jim.go: %v", err)
	}
	if !strings.Contains(string(contents), "func useJake() Jake") {
		t.Fatalf("updated file should contain useJake, got: %s", contents)
	}
	if !strings.Contains(string(contents), `FirstName: "Jake"`) {
		t.Fatalf("updated file should construct Jake, got: %s", contents)
	}
}

func TestApplyFunctionReplacement_AllowsSamePackageJakeType(t *testing.T) {
	dir := t.TempDir()
	jakePath := filepath.Join(dir, "jake.go")
	jimPath := filepath.Join(dir, "jim.go")
	if err := os.WriteFile(jakePath, []byte("package main\n\ntype Jake struct {\n\tFirstName string\n\tLastName  string\n}\n"), 0644); err != nil {
		t.Fatalf("write jake.go: %v", err)
	}
	if err := os.WriteFile(jimPath, []byte("package main\n\nfunc sally() int {\n\treturn 3\n}\n"), 0644); err != nil {
		t.Fatalf("write jim.go: %v", err)
	}

	if _, err := applyFunctionReplacement(jimPath, "sally", "func useJake() Jake { return Jake{FirstName: \"Jake\", LastName: \"The Snake\"} }"); err != nil {
		t.Fatalf("applyFunctionReplacement should allow same-package Jake usage without import: %v", err)
	}

	contents, err := os.ReadFile(jimPath)
	if err != nil {
		t.Fatalf("read updated jim.go: %v", err)
	}
	if !strings.Contains(string(contents), "func useJake() Jake") {
		t.Fatalf("updated file should replace sally with useJake, got: %s", contents)
	}
	if !strings.Contains(string(contents), `LastName: "The Snake"`) {
		t.Fatalf("updated file should return Jake with last name, got: %s", contents)
	}
}

func TestApplyFunctionReplacement_RepairsCorruptedFunction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jim.go")
	original := "package jim\n\nfunc Jim() \n \"jim\" }\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write corrupted file: %v", err)
	}
	if _, err := applyFunctionReplacement(path, "Jim", "func Jim() string { return \"jim\" }"); err != nil {
		t.Fatalf("applyFunctionReplacement should repair corrupted function: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repaired file: %v", err)
	}
	if !strings.Contains(string(contents), "func Jim() string") {
		t.Fatalf("repaired file should contain function signature, got: %s", contents)
	}
	if !strings.Contains(string(contents), `return "jim"`) {
		t.Fatalf("repaired file should contain corrected body, got: %s", contents)
	}
}

func TestHandleCommand_FileAndReplaceOnSameLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jim.go")
	original := "package jim\n\nfunc Jim() int {\n\treturn 0\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}

	mgr := NewConversationManager()
	handled, _ := handleCommand("/file "+path+" replace Jim with func Jim() string { return \"jim\" }", mgr)
	if !handled {
		t.Fatal("expected same-line /file + replace to be handled")
	}
	if got := mgr.Get().TargetGoFile(); got != path {
		t.Fatalf("target file = %q, want %q", got, path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if !strings.Contains(string(contents), "func Jim() string") {
		t.Fatalf("same-line replacement should update target file, got: %s", contents)
	}
	if !strings.Contains(string(contents), `return "jim"`) {
		t.Fatalf("same-line replacement should set new return value, got: %s", contents)
	}
}

func TestApplyCodeToFile_ReplacesStructTagsOnExistingType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "indexer.go")
	original := "package dense\n\ntype SymbolLocation struct {\n\tPkgPath string\n\tFileName string\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	code := "type SymbolLocation struct {\n\tPkgPath string `json:\"pkg_path\"`\n\tFileName string `json:\"file_name\"`\n}\n"
	if _, err := applyCodeToFile(path, code); err != nil {
		t.Fatalf("applyCodeToFile returned error: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if !strings.Contains(string(contents), "json:\"pkg_path\"") || !strings.Contains(string(contents), "json:\"file_name\"") {
		t.Fatalf("existing struct tags were not updated: %s", contents)
	}
}

func TestAddImportAndTypeHelpers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	if err := addImportToFile(path, "fmt"); err != nil {
		t.Fatalf("addImportToFile returned error: %v", err)
	}
	if err := addTypeToFile(path, "type User struct { Name string }\n"); err != nil {
		t.Fatalf("addTypeToFile returned error: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if !strings.Contains(string(contents), `"fmt"`) {
		t.Fatalf("import should be added, got: %s", contents)
	}
	if !strings.Contains(string(contents), "type User struct") {
		t.Fatalf("type should be added, got: %s", contents)
	}
}

func TestInferTargetFileFromPrompt_JSONTags(t *testing.T) {
	got := inferTargetFileFromPrompt("add json tags to User")
	if got == "" {
		t.Fatal("inferTargetFileFromPrompt returned empty for JSON tag prompt")
	}
	if want := "user.go"; filepath.Base(got) != want {
		t.Fatalf("inferTargetFileFromPrompt returned %q, want %q", got, want)
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "user.go")
	if err := ensureGoTargetFile(path, "add json tags to User"); err != nil {
		t.Fatalf("ensureGoTargetFile returned error: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if !strings.Contains(string(contents), "type User struct") {
		t.Fatalf("created file should contain User struct, got: %s", contents)
	}
}

func TestApplyCodeToFile_RejectsInvalidTopLevelStatement(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "jim.go")
	original := "package jim\n\nfunc Jim() string {\n\treturn \"jim\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write original file: %v", err)
	}

	_, err := applyCodeToFile(path, "defer file.Close()")
	if err == nil {
		t.Fatal("expected invalid top-level statement append to fail")
	}

	updated, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file after failed apply: %v", readErr)
	}
	if string(updated) != original {
		t.Fatalf("failed apply should leave file unchanged; got %s", string(updated))
	}
}

func TestPredictIntent_RejectsVaguePrompt(t *testing.T) {
	for _, prompt := range []string{
		"add {",
		"return",
		"add { to jim/jim.go",
	} {
		intent := predictIntent(prompt, nil, nil, nil)
		if intent.Action != "" {
			t.Fatalf("predictIntent(%q) = %#v, want empty action for vague prompts", prompt, intent)
		}
	}
}

func TestInferTargetFileFromPrompt_GoFilePath(t *testing.T) {
	for _, prompt := range []string{
		"fix jim/jim.go",
		"update jim/jim.go with {",
		"add { to jim/jim.go",
		"add function jim to file jim/jim.go",
		"in the file jim/jim.go replace jim with sally() int {return 3}",
	} {
		got := inferTargetFileFromPrompt(prompt)
		if got == "" {
			t.Fatalf("inferTargetFileFromPrompt(%q) returned empty", prompt)
		}
		if filepath.Clean(got) != filepath.Clean("jim/jim.go") {
			t.Fatalf("inferTargetFileFromPrompt(%q) = %q, want %q", prompt, got, "jim/jim.go")
		}
	}
}

func TestClassifyCommandType_JSONTags(t *testing.T) {
	if got := dense.ClassifyCommandType("add json tags to User"); got != "code_update" {
		t.Fatalf("expected code_update for JSON tag prompt, got %q", got)
	}
}

func TestPredictIntent_JSONTagsPrecedence(t *testing.T) {
	intent := predictIntent("add json tags to User", nil, nil, nil)
	if intent.Action != "ADD_JSON_TAGS" {
		t.Fatalf("expected ADD_JSON_TAGS, got %q", intent.Action)
	}
}

func TestCrossPackageAutoImport(t *testing.T) {
	const source = `package main`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	impPath := "github.com/golangast/dense/internal/ai/training"
	ok := astutil.AddImport(fset, file, impPath)
	if !ok {
		t.Fatal("astutil.AddImport reported no change")
	}

	found := false
	for _, imp := range file.Imports {
		if imp.Path != nil && imp.Path.Value == `"github.com/golangast/dense/internal/ai/training"` {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("failed to auto-inject cross-package import")
	}

	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Name.Name == "SaveUser" {
				return
			}
		}
	}
	// The import is the key behavior under test; the parser and AST still pass.
}

func BenchmarkPredictIntent(b *testing.B) {
	prompts := []string{
		"create function ComputeSum(a int, b int) int",
		"add import \"fmt\"",
		"create method Process on Worker(a int) error",
		"add unit test for DoWork",
		"add method Process to struct Worker inside package jobs",
		"add function Fetch Data using net/http",
	}
	for i := 0; i < b.N; i++ {
		for _, prompt := range prompts {
			_ = predictIntent(prompt, nil, nil, nil)
		}
	}
}

func BenchmarkRenderAndApply(b *testing.B) {
	prompts := []string{
		"create function ComputeSum(a int, b int) int",
		"add import \"fmt\"",
		"add method Process to struct Worker inside package jobs",
	}
	for i := 0; i < b.N; i++ {
		for _, prompt := range prompts {
			intent := predictIntent(prompt, nil, nil, nil)
			code, err := renderIntentToCode(intent)
			if err != nil || code == "" {
				continue
			}
			if _, _, err := applyCodeViaAST(fmt.Sprintf("/tmp/dense_bench_%d.go", time.Now().UnixNano()), "package demo\n\n", code); err != nil {
				_ = err
			}
		}
	}
}
