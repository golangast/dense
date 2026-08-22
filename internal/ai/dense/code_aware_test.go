package dense_test

import (
	"fmt"
	"go/ast"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
	"github.com/golangast/dense/internal/generator"
)

func TestCodeAwareInterfaceAndConstructor(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Symbols: map[string]*dense.SymbolRef{
			"Jake": {Name: "Jake", FilePath: "jim/jake.go"},
		},
	}

	// Test 1: Interface implementation prompt
	slot1 := dense.ParseCodeAwarePrompt("implement Stringer for Jake", graph)
	if slot1.Action != "ADD_METHOD" || slot1.TargetSymbol != "Jake" {
		t.Fatalf("Failed to parse interface implementation slot: %+v", slot1)
	}

	// Test 2: Constructor generation prompt
	slot2 := dense.ParseCodeAwarePrompt("generate constructor for Jake", graph)
	if slot2.Action != "GENERATE_CONSTRUCTOR" || slot2.TargetSymbol != "Jake" {
		t.Fatalf("Failed to parse constructor generation slot: %+v", slot2)
	}
}

func TestCodeAwareFixAndGenerateIntent(t *testing.T) {
	tests := []struct {
		prompt      string
		expectedCmd string
	}{
		{"fix compilation errors in user.go", "cmd_fix"},
		{"generate http handler for struct User", "cmd_generate"},
		{"scaffold restful api from model", "cmd_scaffold"},
	}

	for _, tt := range tests {
		cmd := dense.RouteCodeIntent(tt.prompt)
		if cmd != tt.expectedCmd {
			t.Fatalf("For prompt %q, expected %s, got %s", tt.prompt, tt.expectedCmd, cmd)
		}
	}
}
func TestEndToEndFixWorkflow(t *testing.T) {
	tmpDir := t.TempDir()
	brokenFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(brokenFile, []byte("func main() { fmt.Println(x) }"), 0644); err != nil {
		t.Fatalf("write broken file: %v", err)
	}

	errs, err := generator.DiagnoseProject(tmpDir)
	if err != nil {
		t.Fatalf("DiagnoseProject unexpected error: %v", err)
	}
	if len(errs) == 0 {
		t.Fatalf("expected compiler errors for broken file, got 0")
	}
}
func TestCodeAwareAddStructKeepsTypeAction(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Symbols: map[string]*dense.SymbolRef{
			"Jake": {Name: "Jake", FilePath: "jim/jake.go"},
		},
		Files: map[string]*ast.File{
			"jim/jake.go": {Name: ast.NewIdent("main")},
		},
	}

	slot := dense.ParseCodeAwarePrompt("add struct Jaker to jim/jake.go", graph)
	if slot.Action != "ADD_TYPE" {
		t.Fatalf("expected ADD_TYPE, got %q", slot.Action)
	}
	if slot.TargetSymbol != "Jaker" {
		t.Fatalf("expected target symbol Jaker, got %q", slot.TargetSymbol)
	}
	if slot.ExplicitFile != "jim/jake.go" {
		t.Fatalf("expected explicit file jim/jake.go, got %q", slot.ExplicitFile)
	}
	if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jake.go", slot); !ok {
		t.Fatal("expected add-struct route to succeed")
	}
}

func TestCodeAwareAddStructWithFields(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jake.go": {Name: ast.NewIdent("main")},
		},
	}

	slot := dense.ParseCodeAwarePrompt("add struct eeid with the fields name string age int to jim/jake.go", graph)
	if slot.Action != "ADD_TYPE" {
		t.Fatalf("expected ADD_TYPE, got %q", slot.Action)
	}
	if slot.TargetSymbol != "Eeid" {
		t.Fatalf("expected target symbol Eeid, got %q", slot.TargetSymbol)
	}
	if !strings.Contains(slot.PayloadCode, "Name string") || !strings.Contains(slot.PayloadCode, "Age int") {
		t.Fatalf("expected generated field list in payload, got %q", slot.PayloadCode)
	}
	if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jake.go", slot); !ok {
		t.Fatal("expected struct-with-fields route to succeed")
	}
}

func TestCodeAwareAddVarAndSliceDecls(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	for _, tc := range []struct {
		prompt   string
		action   string
		contains string
	}{
		{prompt: "add var s []string to jim/jim.go", action: "ADD_VAR", contains: "var S []string"},
		{prompt: "add slice values []string to jim/jim.go", action: "ADD_VAR", contains: "var Values []string"},
		{prompt: "add m := make(map[string]int) to jim/jim.go", action: "ADD_VAR", contains: "var M = make(map[string]int)"},
		{prompt: "add const answer = 42 to jim/jim.go", action: "ADD_CONST", contains: "const Answer = 42"},
		{prompt: "add if true { println(\"ok\") } to jim/jim.go", action: "ADD_STMT", contains: "func AutoAdded()"},
	} {
		slot := dense.ParseCodeAwarePrompt(tc.prompt, graph)
		if slot.Action != tc.action {
			t.Fatalf("expected %s, got %q for %q", tc.action, slot.Action, tc.prompt)
		}
		if !strings.Contains(slot.PayloadCode, tc.contains) {
			t.Fatalf("expected payload to contain %q, got %q for %q", tc.contains, slot.PayloadCode, tc.prompt)
		}
		if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
			t.Fatalf("expected route to succeed for %q", tc.prompt)
		}
	}
}

func TestCodeAwareListenAndServePrompt(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	for _, prompt := range []string{
		"add ListenAndServe to jim/jim.go",
		"add http.ListenAndServe to jim/jim.go",
		"add HandleFunc to jim/jim.go",
		"add http.HandleFunc to jim/jim.go",
		"add mux.HandleFunc to jim/jim.go",
		"add hello world to jim/jim.go",
		"add http server to jim/jim.go",
		"add gorilla mux router to jim/jim.go",
		"add mysql database to jim/jim.go",
		"add templates to jim/jim.go",
		"add static files to jim/jim.go",
		"add form to jim/jim.go",
		"add middleware to jim/jim.go",
		"add sessions to jim/jim.go",
		"add json to jim/jim.go",
		"add websocket to jim/jim.go",
		"add password hashing to jim/jim.go",
	} {
		slot := dense.ParseCodeAwarePrompt(prompt, graph)
		if slot.Action == "" {
			t.Fatalf("expected non-empty slot action for %q", prompt)
		}
		if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
			t.Fatalf("expected route to succeed for %q (action=%q payload=%q)", prompt, slot.Action, slot.PayloadCode)
		}
	}
}
func TestCodeAwareDocumentCategoryCoverage(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	for _, prompt := range []string{
		"add go advocates to jim/jim.go",
		"add official resources to jim/jim.go",
		"add http package to jim/jim.go",
		"add database get post to jim/jim.go",
		"add framework examples to jim/jim.go",
		"add testing go code to jim/jim.go",
		"add reader type to jim/jim.go",
		"add context logging to jim/jim.go",
		"add interfaces 2 to jim/jim.go",
		"add syntax to jim/jim.go",
		"add go wasm to jim/jim.go",
		"add example projects to jim/jim.go",
		"add docker to jim/jim.go",
		"add containers to jim/jim.go",
		"add ast package to jim/jim.go",
		"add interview to jim/jim.go",
		"add go jobs to jim/jim.go",
	} {
		slot := dense.ParseCodeAwarePrompt(prompt, graph)
		if slot.Action == "" {
			t.Fatalf("expected non-empty slot action for %q", prompt)
		}
		if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
			t.Fatalf("expected route to succeed for %q (action=%q payload=%q)", prompt, slot.Action, slot.PayloadCode)
		}
	}
}
func TestCodeAwareStudyPlanCoverage(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	for _, prompt := range []string{
		"add Rob Pike to jim/jim.go",
		"add Dave Cheney to jim/jim.go",
		"add Russ Cox to jim/jim.go",
		"add Bill Kennedy to jim/jim.go",
		"add Todd McLeod to jim/jim.go",
		"add interfaces and reader types to jim/jim.go",
		"add memory and types to jim/jim.go",
		"add error handling and context to jim/jim.go",
		"add http routing and middleware to jim/jim.go",
		"add database sql and gRPC to jim/jim.go",
		"add Dockerfile and docker compose to jim/jim.go",
		"add cobra CLI and go ast to jim/jim.go",
		"add unit testing and pprof to jim/jim.go",
		"add Svelte frontend and CORS to jim/jim.go",
		"add static assets and templates to jim/jim.go",
	} {
		slot := dense.ParseCodeAwarePrompt(prompt, graph)
		if slot.Action == "" {
			t.Fatalf("expected non-empty slot action for %q", prompt)
		}
		if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
			t.Fatalf("expected route to succeed for %q (action=%q payload=%q)", prompt, slot.Action, slot.PayloadCode)
		}
	}
}

func TestCodeAwareResourceStrategyAndPhases(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	for _, prompt := range []string{
		"add core principles and fundamentals to jim/jim.go",
		"add deep dives and internal mechanics to jim/jim.go",
		"add reference and practical recipes to jim/jim.go",
		"add phase 1 build a skeleton to jim/jim.go",
		"add phase 2 project based active recall to jim/jim.go",
		"add phase 3 feynman technique and in depth study to jim/jim.go",
		"add phase 4 spaced repetition for interview and syntax mastery to jim/jim.go",
		"add effective go and tour of go to jim/jim.go",
		"add golang standards project layout and slice tricks to jim/jim.go",
	} {
		slot := dense.ParseCodeAwarePrompt(prompt, graph)
		if slot.Action == "" {
			t.Fatalf("expected non-empty slot action for %q", prompt)
		}
		if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
			t.Fatalf("expected route to succeed for %q (action=%q payload=%q)", prompt, slot.Action, slot.PayloadCode)
		}
	}
}

func TestCodeAwareTutorialURLExample(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html><body>
				<pre><code>package main
				import "fmt"
				func sayHi() {
					fmt.Println("hi")
				}</code></pre>
			</body></html>`))
	}))
	defer server.Close()

	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	prompt := fmt.Sprintf("add example from %s to jim/jim.go", server.URL)
	slot := dense.ParseCodeAwarePrompt(prompt, graph)
	if slot.Action != "ADD_DECL" {
		t.Fatalf("expected ADD_DECL for tutorial URL, got %q (payload=%q)", slot.Action, slot.PayloadCode)
	}
	if !strings.Contains(slot.PayloadCode, "func sayHi") {
		t.Fatalf("expected extracted tutorial code in payload, got %q", slot.PayloadCode)
	}
	if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
		t.Fatal("expected external tutorial example route to succeed")
	}
}

func TestCodeAwareURLSnippetWithoutPackageOrFunc(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html><body>
				<pre><code>
				s := "some string"
				b := []byte(s)
				s2 := string(b)
				</code></pre>
			</body></html>`))
	}))
	defer server.Close()

	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	prompt := fmt.Sprintf("add example from %s to jim/jim.go", server.URL)
	slot := dense.ParseCodeAwarePrompt(prompt, graph)
	if slot.Action != "ADD_DECL" {
		t.Fatalf("expected ADD_DECL for short Go snippet URL, got %q (payload=%q)", slot.Action, slot.PayloadCode)
	}
	if !strings.Contains(slot.PayloadCode, "s := \"some string\"") {
		t.Fatalf("expected extracted short Go snippet in payload, got %q", slot.PayloadCode)
	}
	if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
		t.Fatal("expected route to succeed for short Go snippet URL")
	}
}

func TestCodeAwareGoByExampleCoverage(t *testing.T) {
	graph := &dense.WorkspaceGraph{
		Files: map[string]*ast.File{
			"jim/jim.go": {Name: ast.NewIdent("main")},
		},
	}

	for _, prompt := range []string{
		"add var msg string to jim/jim.go",
		"add const answer = 42 to jim/jim.go",
		"add value 42 to jim/jim.go",
		"add for i := 0; i < 3; i++ { println(i) } to jim/jim.go",
		"add if x > 0 { println(\"x\") } to jim/jim.go",
		"add switch x { case 1: println(\"one\"); default: println(\"other\") } to jim/jim.go",
		"add []string{\"a\", \"b\"} to jim/jim.go",
		"add map scores map[string]int to jim/jim.go",
		"add map[string]int{\"a\": 1} to jim/jim.go",
		"add func greet(name string) string { return \"hi\" } to jim/jim.go",
		"add func sum(a, b int) (int, int) { return a + b, a * b } to jim/jim.go",
		"add func add(nums ...int) int { total := 0; for _, n := range nums { total += n }; return total } to jim/jim.go",
		"add func fib(n int) int { if n <= 1 { return n }; return fib(n-1) + fib(n-2) } to jim/jim.go",
		"add for _, v := range []int{1, 2, 3} { println(v) } to jim/jim.go",
		"add p := &value to jim/jim.go",
		"add type Person struct { Name string } to jim/jim.go",
		"add interface Greeter interface { Greet() string } to jim/jim.go",
		"add type MyError struct { Msg string } to jim/jim.go",
		"add context context.Background() to jim/jim.go",
		"add go func() { println(\"hi\") }() to jim/jim.go",
		"add ch := make(chan int, 2) to jim/jim.go",
		"add select { case msg := <-ch: println(msg); default: println(\"no\") } to jim/jim.go",
		"add the closure intSeq to jim/jim.go",
		"add hello world to jim/jim.go",
		"add http server to jim/jim.go",
		"add gorilla mux router to jim/jim.go",
		"add mysql database to jim/jim.go",
		"add templates to jim/jim.go",
		"add static files to jim/jim.go",
		"add form to jim/jim.go",
		"add middleware to jim/jim.go",
		"add sessions to jim/jim.go",
		"add json to jim/jim.go",
		"add websocket to jim/jim.go",
		"add password hashing to jim/jim.go",
	} {
		slot := dense.ParseCodeAwarePrompt(prompt, graph)
		if slot.Action == "" {
			t.Fatalf("expected non-empty slot action for %q", prompt)
		}
		if _, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "jim/jim.go", slot); !ok {
			t.Fatalf("expected route to succeed for %q (action=%q payload=%q)", prompt, slot.Action, slot.PayloadCode)
		}
	}
}
