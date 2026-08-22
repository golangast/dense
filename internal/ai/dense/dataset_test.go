package dense

import "testing"

func TestTokenizeForBagOfWords_SplitsIdentifiersAndSubwords(t *testing.T) {
	tokens := TokenizeForBagOfWords("ValidateUser compute_sum DoWork")
	want := map[string]bool{
		"validate": true,
		"user":     true,
		"compute":  true,
		"sum":      true,
		"do":       true,
		"work":     true,
		"val":      true,
		"ali":      true,
		"lid":      true,
	}
	for _, tok := range tokens {
		if want[tok] {
			delete(want, tok)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing expected tokens: %#v, got %#v", want, tokens)
	}
}

func TestExtractASTContextFeatures(t *testing.T) {
	src := `package demo

import (
	"fmt"
	"context"
)

func ValidateUser(ctx context.Context, id string) error {
	if err != nil {
		return err
	}
	return nil
}

type User struct {
	Name string
}

type Runner interface {
	Run() error
}
`
	ctx := ExtractASTContextFeatures(src)
	if !ctx.HasPackageDecl {
		t.Fatal("expected package declaration flag")
	}
	if !ctx.InsideFunc {
		t.Fatal("expected inside_func flag")
	}
	if !ctx.InsideStruct {
		t.Fatal("expected inside_struct flag")
	}
	if !ctx.InsideInterface {
		t.Fatal("expected inside_interface flag")
	}
	if !ctx.HasImportFmt || !ctx.HasImportContext || ctx.HasImportTesting {
		t.Fatalf("unexpected import flags: %#v", ctx)
	}
	if ctx.FuncCount != 1 || ctx.TypeCount != 2 || ctx.ReturnCheckCount != 1 {
		t.Fatalf("unexpected AST counts: %#v", ctx)
	}
}
