package main

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		sourceCode string
		wantAction string
	}{
		{
			name:   "Multiple Receivers / Type Overloading",
			prompt: "add method String() string to Worker",
			sourceCode: `package main

type Worker struct{}
type Admin struct{}`,
			wantAction: "ADD_FUNC",
		},
		{
			name:   "Prevent Duplicate Imports",
			prompt: "add function Fetch using net/http",
			sourceCode: `package main
		import "net/http"`,
			wantAction: "ADD_IMPORT",
		},
		{
			name:       "Anonymous Structs & Generics",
			prompt:     "create function Process[T any](data T) error",
			sourceCode: `package main`,
			wantAction: "ADD_FUNC",
		},
		{
			name:       "Variadic Parameters & Multiple Returns",
			prompt:     "add function Exec(opts ...string) (int, error)",
			sourceCode: `package main`,
			wantAction: "ADD_FUNC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "target.go", tt.sourceCode, parser.ParseComments)
			if err != nil {
				t.Fatalf("Failed to parse base source: %v", err)
			}

			_ = file
			intent := predictIntent(tt.prompt, nil, nil, nil)
			if intent.Action != tt.wantAction {
				t.Errorf("Expected action %s, got %s", tt.wantAction, intent.Action)
			}
		})
	}
}
