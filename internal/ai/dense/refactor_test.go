package dense_test

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

func TestAutoInjectJSONTags(t *testing.T) {
	src := `package main

type User struct {
    FirstName string
    LastName  string
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse Go code: %v", err)
	}

	modified := dense.AutoInjectJSONTags(file, "User")
	if !modified {
		t.Errorf("Expected AST modification, got false")
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatalf("Failed to format modified AST: %v", err)
	}

	expectedSubstring := "FirstName string `json:\"first_name\"`"
	if !bytes.Contains(buf.Bytes(), []byte(expectedSubstring)) {
		t.Errorf("Expected formatted tag %s, got:\n%s", expectedSubstring, buf.String())
	}
}

func TestGenerateConstructor(t *testing.T) {
	src := `package main

type User struct {
    FirstName string
    LastName string
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	if !dense.GenerateConstructor(file, "User") {
		t.Fatal("expected constructor generation to succeed")
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatalf("format generated constructor: %v", err)
	}
	text := buf.String()
	if !bytes.Contains([]byte(text), []byte("func NewUser")) {
		t.Fatalf("generated file missing constructor: %s", text)
	}
}

func TestImplementInterfaceStubs(t *testing.T) {
	src := `package main

type Greeter interface {
    Greet(name string)
}

type User struct {
    Name string
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	if !dense.ImplementInterfaceStubs(file, "Greeter", "User") {
		t.Fatal("expected interface stub generation to succeed")
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatalf("format interface stubs: %v", err)
	}
	text := buf.String()
	if !bytes.Contains([]byte(text), []byte("func (u *User) Greet")) {
		t.Fatalf("generated file missing method stub: %s", text)
	}
}

func TestGenerateTableDrivenTest(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "sample.go", "package main\n\nfunc Add(a, b int) int { return a + b }\n", parser.ParseComments)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	dir := t.TempDir()
	outPath := filepath.Join(dir, "sample_test.go")
	ok, err := dense.GenerateTableDrivenTest(file, "Add", outPath)
	if err != nil || !ok {
		t.Fatalf("GenerateTableDrivenTest returned (%v, %v)", ok, err)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated test: %v", err)
	}
	if !bytes.Contains(b, []byte("func TestAdd")) {
		t.Fatalf("generated test file missing function name: %s", string(b))
	}
}

func TestMarkovPredictor(t *testing.T) {
	p := dense.NewPredictor()
	p.RecordSequence("ADD_STRUCT", "ADD_JSON_TAGS")
	p.RecordSequence("ADD_STRUCT", "ADD_JSON_TAGS")
	p.RecordSequence("ADD_STRUCT", "ADD_FUNC")

	next := p.PredictNext("ADD_STRUCT", 1)
	if len(next) == 0 || next[0] != "ADD_JSON_TAGS" {
		t.Errorf("Expected top prediction 'ADD_JSON_TAGS', got %v", next)
	}
}

func TestResolveStdlibInterface(t *testing.T) {
	sig, ok := dense.ResolveStdlibInterface("io.Reader")
	if !ok {
		t.Fatal("expected io.Reader signature to resolve")
	}
	if sig == "" || !strings.Contains(sig, "Read(p []byte)") {
		t.Fatalf("unexpected signature for io.Reader: %q", sig)
	}
}

func TestAnalyzeFuncIdioms(t *testing.T) {
	src := `package demo

func Process[T any](items ...string) <-chan string { return nil }
`
	file, err := parser.ParseFile(token.NewFileSet(), "demo.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	feats := dense.AnalyzeFuncIdioms(fn)
	if !feats.HasGenerics {
		t.Fatal("expected generic detection")
	}
	if !feats.IsVariadic {
		t.Fatal("expected variadic detection")
	}
	if !feats.HasChannels {
		t.Fatal("expected channel detection")
	}
}

func TestEnsureContextParam(t *testing.T) {
	src := `package demo

func Fetch(url string) error { return nil }
`
	file, err := parser.ParseFile(token.NewFileSet(), "demo.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)
	if !dense.EnsureContextParam(fn) {
		t.Fatal("expected context injection to succeed")
	}
	if len(fn.Type.Params.List) == 0 || fn.Type.Params.List[0].Names[0].Name != "ctx" {
		t.Fatalf("context parameter not injected: %+v", fn.Type.Params.List)
	}
}

func BenchmarkRefactorEngine(b *testing.B) {
	src := `package main

type User struct {
    FirstName string
    LastName string
}

type Greeter interface {
    Greet(name string)
}

func Add(a, b int) int { return a + b }
`
	fset := token.NewFileSet()
	for i := 0; i < b.N; i++ {
		file, err := parser.ParseFile(fset, "bench.go", src, parser.ParseComments)
		if err != nil {
			b.Fatalf("parse source: %v", err)
		}
		_ = dense.AutoInjectJSONTags(file, "User")
		_ = dense.GenerateConstructor(file, "User")
		_ = dense.ImplementInterfaceStubs(file, "Greeter", "User")
		_, _ = dense.GenerateTableDrivenTest(file, "Add", filepath.Join(b.TempDir(), "bench_test.go"))
	}
}
