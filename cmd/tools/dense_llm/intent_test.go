package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func wrap(src string) string {
	// ensure package wrapper so parser can parse imports/types/tests
	if strings.HasPrefix(strings.TrimSpace(src), "package ") {
		return src
	}
	return "package p\n\n" + src
}

func TestPredictIntent_AddFunc(t *testing.T) {
	intent := predictIntent("create function Validate", nil, nil, nil)
	if intent.Action != "ADD_FUNC" {
		t.Fatalf("expected ADD_FUNC, got %q", intent.Action)
	}
	if intent.Name != "Validate" {
		t.Fatalf("expected name Validate, got %q", intent.Name)
	}
}

func TestRenderIntentToCode_AddFunc(t *testing.T) {
	intent := Intent{Action: "ADD_FUNC", Name: "Validate"}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	src := wrap(code)
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", src, parser.ParseComments); err != nil {
		t.Fatalf("rendered function not parseable: %v\ncode:\n%s", err, code)
	}
}

func TestPredictIntent_AddImportAndRender(t *testing.T) {
	intent := predictIntent("add import \"fmt\"", nil, nil, nil)
	if intent.Action != "ADD_IMPORT" {
		t.Fatalf("expected ADD_IMPORT, got %q", intent.Action)
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	src := wrap(code)
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", src, parser.ParseComments); err != nil {
		t.Fatalf("rendered import not parseable: %v\ncode:\n%s", err, code)
	}
}

func TestPredictIntent_AddTestAndRender(t *testing.T) {
	intent := predictIntent("add unit test for DoWork", nil, nil, nil)
	if intent.Action != "ADD_TEST" {
		t.Fatalf("expected ADD_TEST, got %q", intent.Action)
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	src := wrap(code)
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", src, parser.ParseComments); err != nil {
		t.Fatalf("rendered test not parseable: %v\ncode:\n%s", err, code)
	}
}

func TestPredictIntent_FuncWithParamsAndReturn(t *testing.T) {
	intent := predictIntent("create function ComputeSum(a int, b int) int", nil, nil, nil)
	if intent.Action != "ADD_FUNC" {
		t.Fatalf("expected ADD_FUNC, got %q", intent.Action)
	}
	if len(intent.Params) != 2 || intent.Params[0] != "a int" || intent.Params[1] != "b int" {
		t.Fatalf("unexpected params: %#v", intent.Params)
	}
	if len(intent.Returns) != 1 || intent.Returns[0] != "int" {
		t.Fatalf("unexpected returns: %#v", intent.Returns)
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	src := wrap(code)
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", src, parser.ParseComments); err != nil {
		t.Fatalf("rendered function not parseable: %v\ncode:\n%s", err, code)
	}
}

func TestPredictIntent_AddStructWithFieldsAndRender(t *testing.T) {
	intent := predictIntent("add struct Jake with fields name string age int to file jim/jake.go", nil, nil, nil)
	if intent.Action != "ADD_TYPE" {
		t.Fatalf("expected ADD_TYPE, got %q", intent.Action)
	}
	if intent.Name != "Jake" {
		t.Fatalf("expected name Jake, got %q", intent.Name)
	}
	if len(intent.Params) != 2 || intent.Params[0] != "name string" || intent.Params[1] != "age int" {
		t.Fatalf("unexpected struct fields: %#v", intent.Params)
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if !strings.Contains(code, "name") || !strings.Contains(code, "string") || !strings.Contains(code, "age") || !strings.Contains(code, "int") {
		t.Fatalf("rendered struct does not include fields: %s", code)
	}
}

func TestPredictIntent_ImportStructFromFilePath(t *testing.T) {
	intent := predictIntent("import the struct Jake from jim/jake.go to jim/jim.go", nil, nil, nil)
	if intent.Action != "ADD_IMPORT" {
		t.Fatalf("expected ADD_IMPORT, got %q", intent.Action)
	}
	if intent.Name == "" || strings.Contains(intent.Name, ".go") {
		t.Fatalf("import path should not include .go, got %q", intent.Name)
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if !strings.Contains(code, "\"") || !strings.Contains(code, "jim") {
		t.Fatalf("rendered import should contain a package path, got %s", code)
	}
}

func TestPredictIntent_ReceiverAndRender(t *testing.T) {
	intent := predictIntent("create method DoThing on Service(a int) error", nil, nil, nil)
	if intent.Action != "ADD_FUNC" {
		t.Fatalf("expected ADD_FUNC, got %q", intent.Action)
	}
	if intent.Receiver == "" {
		t.Fatalf("expected receiver to be detected, got empty")
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	src := wrap(code)
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", src, parser.ParseComments); err != nil {
		t.Fatalf("rendered method not parseable: %v\ncode:\n%s", err, code)
	}
}

func TestPredictIntent_MapSliceUnnamedParams(t *testing.T) {
	intent := predictIntent("create function Process(items []string, m map[string]int) error", nil, nil, nil)
	if intent.Action != "ADD_FUNC" {
		t.Fatalf("expected ADD_FUNC, got %q", intent.Action)
	}
	if len(intent.Params) != 2 {
		t.Fatalf("expected 2 params, got %#v", intent.Params)
	}
	if len(intent.Returns) != 1 {
		t.Fatalf("expected 1 return, got %#v", intent.Returns)
	}
	code, err := renderIntentToCode(intent)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	src := wrap(code)
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "", src, parser.ParseComments); err != nil {
		t.Fatalf("rendered function not parseable: %v\ncode:\n%s", err, code)
	}
}
