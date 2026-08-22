package dense_test

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

func TestContextAndErrorRefactoring(t *testing.T) {
	src := `package main
func Greet() string { return "hello" }
func ProcessOrder() error {
    err := doSomething()
    return err
}`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatalf("Failed to parse src: %v", err)
	}

	// Test 1: Add context to Greet
	if !dense.EnsureContextParamInFile(file, "Greet") {
		t.Fatalf("EnsureContextParamInFile failed")
	}

	// Test 2: Wrap return errors in ProcessOrder
	if !dense.WrapReturnErrorsInFile(file, "ProcessOrder") {
		t.Fatalf("WrapReturnErrorsInFile failed")
	}
}
