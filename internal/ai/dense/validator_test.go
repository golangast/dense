package dense

import (
	"errors"
	"testing"
)

func TestIsExternalImportError(t *testing.T) {
	if !IsExternalImportError(errors.New("could not import fmt")) {
		t.Fatal("expected legacy import error to be tolerated")
	}
	if !IsExternalImportError(errors.New("cannot find package github.com/example/lib")) {
		t.Fatal("expected missing package error to be tolerated")
	}
	if IsExternalImportError(errors.New("undefined: Foo")) {
		t.Fatal("expected structural type errors to remain strict")
	}
}

func TestValidateASTWithTolerances(t *testing.T) {
	if err := ValidateASTWithTolerances(nil, []error{errors.New("could not import foo/bar")}); err != nil {
		t.Fatalf("expected external import errors to be ignored, got %v", err)
	}
	if err := ValidateASTWithTolerances(nil, []error{errors.New("undefined: Foo")}); err == nil {
		t.Fatal("expected real structural errors to fail validation")
	}
}
