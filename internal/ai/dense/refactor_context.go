package dense

import (
	"fmt"
	"go/ast"
	"go/token"
)

// EnsureContextParamInFile prepends `ctx context.Context` as the first parameter
// of a named function inside the given file AST.
func EnsureContextParamInFile(file *ast.File, targetFunc string) bool {
	if file == nil || targetFunc == "" {
		return false
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == targetFunc {
			// reuse existing helper that operates on *ast.FuncDecl
			if EnsureContextParam(fn) {
				EnsureImport(file, "context")
				return true
			}
			return false
		}
	}
	return false
}

// WrapReturnErrorsInFile wraps return errors in the named function with fmt.Errorf
// by delegating to the existing WrapErrorsInFunc helper and ensuring imports.
func WrapReturnErrorsInFile(file *ast.File, targetFunc string) bool {
	if file == nil || targetFunc == "" {
		return false
	}
	modified := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != targetFunc || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			for i, expr := range ret.Results {
				if ident, ok := expr.(*ast.Ident); ok && ident.Name == "err" {
					// Replace with fmt.Errorf("<func>: %w", err)
					ret.Results[i] = &ast.CallExpr{
						Fun: &ast.SelectorExpr{X: ast.NewIdent("fmt"), Sel: ast.NewIdent("Errorf")},
						Args: []ast.Expr{
							&ast.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("%q", targetFunc+" error: %w")},
							ast.NewIdent("err"),
						},
					}
					modified = true
				}
			}
			return true
		})
	}
	if modified {
		EnsureImport(file, "fmt")
	}
	return modified
}

// EnsureImport adds an import if not present in the AST file.
func EnsureImport(file *ast.File, importPath string) {
	if file == nil || importPath == "" {
		return
	}
	// check existing imports
	for _, imp := range file.Imports {
		if imp.Path != nil && imp.Path.Value == fmt.Sprintf("%q", importPath) {
			return
		}
	}
	newImport := &ast.ImportSpec{
		Path: &ast.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("%q", importPath)},
	}
	// append to first import decl if exists
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			gen.Specs = append(gen.Specs, newImport)
			return
		}
	}
	// create new import decl at top
	file.Decls = append([]ast.Decl{&ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{newImport}}}, file.Decls...)
}
