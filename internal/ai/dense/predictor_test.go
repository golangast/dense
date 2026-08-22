package dense

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCommandPredictorRecordsTransitions(t *testing.T) {
	p := NewPredictor()
	p.RecordSequence("add_func", "add_test")
	p.RecordSequence("add_func", "add_test")
	p.RecordSequence("add_func", "add_import")

	got := p.PredictNext("add_func", 2)
	if len(got) != 2 || got[0] != "add_test" {
		t.Fatalf("PredictNext(add_func, 2) = %#v, want [add_test add_import]", got)
	}

	if got := p.PredictNext("missing", 1); len(got) == 0 || got[0] != "add function" {
		t.Fatalf("PredictNext(missing, 1) = %#v, want fallback suggestions", got)
	}
}

func TestTokenizeCodePromptAndDistance(t *testing.T) {
	words := TokenizeCodePrompt("ValidateUserProfile")
	if len(words) != 3 || words[0] != "validate" || words[1] != "user" || words[2] != "profile" {
		t.Fatalf("TokenizeCodePrompt returned %#v, want [validate user profile]", words)
	}

	if got := LevenshteinDistance("add test", "add tests"); got > 2 {
		t.Fatalf("LevenshteinDistance too high: got %d", got)
	}
}

func TestPredictorSaveAndLoad(t *testing.T) {
	p := NewPredictor()
	p.RecordSequence("ADD_FUNC", "add unit test")
	p.RecordSequence("ADD_FUNC", "add unit test")
	p.RecordSequence("ADD_FUNC", "add error check")

	if got := p.PredictNext("ADD_FUNC", 2); len(got) != 2 || got[0] != "add unit test" {
		t.Fatalf("PredictNext(ADD_FUNC, 2) = %#v, want [add unit test add error check]", got)
	}

	path := t.TempDir() + "/predictor.json"
	if err := p.SaveToFile(path); err != nil {
		t.Fatalf("SaveToFile returned err: %v", err)
	}
	loaded := NewPredictor()
	if err := loaded.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile returned err: %v", err)
	}
	if got := loaded.PredictNext("ADD_FUNC", 1); len(got) != 1 || got[0] != "add unit test" {
		t.Fatalf("loaded predictor suggested %#v, want [add unit test]", got)
	}
}

func TestAddJSONTagsToStruct(t *testing.T) {
	src := `package demo

type User struct {
	Name string
	Age int
}`
	file, err := parser.ParseFile(token.NewFileSet(), "demo.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	AutoInjectJSONTags(file, "User")
	if file.Decls == nil || len(file.Decls) == 0 {
		t.Fatal("expected declarations to remain present")
	}

	astStruct := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
	if got := astStruct.Type.(*ast.StructType).Fields.List[0].Tag.Value; got == "" {
		t.Fatal("expected json tag to be injected")
	}
}
