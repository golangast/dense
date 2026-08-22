package dense

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var camelRegex = regexp.MustCompile("([a-z0-9])([A-Z])")

func toSnakeCase(str string) string {
	snake := camelRegex.ReplaceAllString(str, "${1}_${2}")
	return strings.ToLower(snake)
}

func toTitleCase(str string) string {
	if str == "" {
		return ""
	}
	return strings.ToUpper(str[:1]) + str[1:]
}

// EnsureContextParam guarantees context.Context is the first argument for I/O operations.
func EnsureContextParam(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Type == nil {
		return false
	}
	if fn.Type.Params == nil {
		fn.Type.Params = &ast.FieldList{}
	}
	if len(fn.Type.Params.List) > 0 {
		if sel, ok := fn.Type.Params.List[0].Type.(*ast.SelectorExpr); ok {
			if ident, ok2 := sel.X.(*ast.Ident); ok2 && ident.Name == "context" && sel.Sel.Name == "Context" {
				return false
			}
		}
	}

	ctxField := &ast.Field{
		Names: []*ast.Ident{ast.NewIdent("ctx")},
		Type: &ast.SelectorExpr{
			X:   ast.NewIdent("context"),
			Sel: ast.NewIdent("Context"),
		},
	}
	fn.Type.Params.List = append([]*ast.Field{ctxField}, fn.Type.Params.List...)
	return true
}

// AutoInjectJSONTags scans the file AST for the target struct and adds `json:"field_name"` tags.
func AutoInjectJSONTags(file *ast.File, structName string) bool {
	if file == nil {
		return false
	}
	modified := false
	ast.Inspect(file, func(n ast.Node) bool {
		typeSpec, ok := n.(*ast.TypeSpec)
		if !ok || typeSpec.Name == nil || typeSpec.Name.Name != structName {
			return true
		}
		structType, ok := typeSpec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range structType.Fields.List {
			if len(field.Names) > 0 && field.Tag == nil {
				fieldName := field.Names[0].Name
				tagValue := fmt.Sprintf("`json:\"%s\"`", toSnakeCase(fieldName))
				field.Tag = &ast.BasicLit{Kind: token.STRING, Value: tagValue}
				modified = true
			}
		}
		return false
	})
	return modified
}

// GenerateConstructor scans a struct and appends a New<StructName>(...) constructor.
func GenerateConstructor(file *ast.File, structName string) bool {
	if file == nil || structName == "" {
		return false
	}
	var target *ast.TypeSpec
	ast.Inspect(file, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok && ts.Name != nil && ts.Name.Name == structName {
			target = ts
			return false
		}
		return true
	})
	if target == nil {
		return false
	}
	structType, ok := target.Type.(*ast.StructType)
	if !ok {
		return false
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name != nil && fn.Name.Name == "New"+structName {
			return false
		}
	}
	params := make([]*ast.Field, 0)
	elts := make([]ast.Expr, 0)
	for _, field := range structType.Fields.List {
		if len(field.Names) == 0 {
			continue
		}
		for _, name := range field.Names {
			paramName := name.Name
			params = append(params, &ast.Field{Names: []*ast.Ident{name}, Type: field.Type})
			elts = append(elts, &ast.KeyValueExpr{Key: ast.NewIdent(paramName), Value: ast.NewIdent(paramName)})
		}
	}
	fn := &ast.FuncDecl{
		Name: ast.NewIdent("New" + structName),
		Type: &ast.FuncType{
			Params:  &ast.FieldList{List: params},
			Results: &ast.FieldList{List: []*ast.Field{{Type: &ast.StarExpr{X: ast.NewIdent(structName)}}}},
		},
		Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{Results: []ast.Expr{&ast.UnaryExpr{Op: token.AND, X: &ast.CompositeLit{Type: ast.NewIdent(structName), Elts: elts}}}}}},
	}
	file.Decls = append(file.Decls, fn)
	return true
}

// ImplementInterfaceStubs inspects an interface declaration and appends method receivers to the target struct.
func ImplementInterfaceStubs(file *ast.File, interfaceName, targetStruct string) bool {
	if file == nil || interfaceName == "" || targetStruct == "" {
		return false
	}
	var iface *ast.TypeSpec
	ast.Inspect(file, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok && ts.Name != nil && ts.Name.Name == interfaceName {
			iface = ts
			return false
		}
		return true
	})
	if iface == nil {
		return false
	}
	intType, ok := iface.Type.(*ast.InterfaceType)
	if !ok {
		return false
	}
	receiverName := strings.ToLower(targetStruct[:1])
	for _, method := range intType.Methods.List {
		if len(method.Names) == 0 {
			continue
		}
		for _, name := range method.Names {
			params := &ast.FieldList{}
			results := &ast.FieldList{}
			if ft, ok := method.Type.(*ast.FuncType); ok {
				params = ft.Params
				results = ft.Results
			}
			body := []ast.Stmt{}
			if results != nil && len(results.List) > 0 {
				zeroVals := make([]ast.Expr, 0, len(results.List))
				for i := range results.List {
					if len(results.List[i].Names) > 0 {
						zeroVals = append(zeroVals, ast.NewIdent("nil"))
						continue
					}
					typeExpr := results.List[i].Type
					if typeExpr == nil {
						zeroVals = append(zeroVals, ast.NewIdent("nil"))
						continue
					}
					switch t := typeExpr.(type) {
					case *ast.Ident:
						if t.Name == "string" {
							zeroVals = append(zeroVals, &ast.BasicLit{Kind: token.STRING, Value: `""`})
						} else if t.Name == "bool" {
							zeroVals = append(zeroVals, ast.NewIdent("false"))
						} else if t.Name == "int" || t.Name == "int64" || t.Name == "float64" {
							zeroVals = append(zeroVals, ast.NewIdent("0"))
						} else {
							zeroVals = append(zeroVals, ast.NewIdent("nil"))
						}
					default:
						zeroVals = append(zeroVals, ast.NewIdent("nil"))
					}
				}
				body = append(body, &ast.ReturnStmt{Results: zeroVals})
			}
			file.Decls = append(file.Decls, &ast.FuncDecl{
				Recv: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent(receiverName)}, Type: &ast.StarExpr{X: ast.NewIdent(targetStruct)}}}},
				Name: ast.NewIdent(name.Name),
				Type: &ast.FuncType{Params: params, Results: results},
				Body: &ast.BlockStmt{List: body},
			})
		}
	}
	return true
}

// GenerateTableDrivenTest writes a table-driven test file for a top-level function.
func GenerateTableDrivenTest(file *ast.File, funcName, outPath string) (bool, error) {
	if file == nil || funcName == "" || outPath == "" {
		return false, fmt.Errorf("invalid inputs")
	}
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == funcName {
			found = true
			return false
		}
		return true
	})
	if !found {
		return false, fmt.Errorf("function %s not found", funcName)
	}
	var buf strings.Builder
	buf.WriteString("package main\n\n")
	buf.WriteString("import \"testing\"\n\n")
	buf.WriteString(fmt.Sprintf("func Test%s(t *testing.T) {\n", toTitleCase(funcName)))
	buf.WriteString("\ttests := []struct {\n")
	buf.WriteString("\t\tname string\n")
	buf.WriteString("\t\tinput int\n")
	buf.WriteString("\t\texpected int\n")
	buf.WriteString("\t}{\n")
	buf.WriteString("\t\t{name: \"case 1\", input: 2, expected: 3},\n")
	buf.WriteString("\t}\n\n")
	buf.WriteString("\tfor _, tt := range tests {\n")
	buf.WriteString("\t\tt.Run(tt.name, func(t *testing.T) {\n")
	buf.WriteString(fmt.Sprintf("\t\t\tif got := %s(tt.input, 1); got != tt.expected {\n", funcName))
	buf.WriteString("\t\t\t\tt.Fatalf(\"%s(%d) = %d, want %d\", tt.input, got, tt.expected)\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t})\n")
	buf.WriteString("\t}\n}\n")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return false, err
	}
	if err := os.WriteFile(outPath, []byte(buf.String()), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// WrapErrorsInFunc scans a specific function and wraps raw `return err` statements with fmt.Errorf.
func WrapErrorsInFunc(file *ast.File, targetFunc string) bool {
	if file == nil {
		return false
	}
	modified := false
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != targetFunc || fn.Body == nil {
			return true
		}
		for _, stmt := range fn.Body.List {
			if ifStmt, ok := stmt.(*ast.IfStmt); ok {
				for _, bodyStmt := range ifStmt.Body.List {
					ret, ok := bodyStmt.(*ast.ReturnStmt)
					if !ok {
						continue
					}
					for i, expr := range ret.Results {
						if id, ok := expr.(*ast.Ident); ok && id.Name == "err" {
							ret.Results[i] = &ast.CallExpr{
								Fun: &ast.SelectorExpr{X: ast.NewIdent("fmt"), Sel: ast.NewIdent("Errorf")},
								Args: []ast.Expr{
									&ast.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("\"%s failed: %%w\"", targetFunc)},
									ast.NewIdent("err"),
								},
							}
							modified = true
						}
					}
				}
			}
		}
		return false
	})
	return modified
}

func init() {
	_ = format.Node
	_ = parser.ParseFile
}

// AppendFunctionDecl parses a raw function snippet and appends it to the file AST.
func AppendFunctionDecl(file *ast.File, rawSnippet string) bool {
	if file == nil || strings.TrimSpace(rawSnippet) == "" {
		return false
	}
	fset := token.NewFileSet()
	// Parse as a small file with package and function(s)
	snippet := strings.TrimSpace(rawSnippet)
	if !strings.HasPrefix(strings.ToLower(snippet), "func ") {
		snippet = "func " + snippet
	}
	parsed, err := parser.ParseFile(fset, "", "package dummy\n"+snippet, 0)
	if err != nil || len(parsed.Decls) == 0 {
		return false
	}

	// Find the first FuncDecl and append it
	for _, d := range parsed.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			file.Decls = append(file.Decls, fd)
			return true
		}
	}
	return false
}

// AppendTypeDecl parses a raw type declaration and appends it to the file AST.
func AppendTypeDecl(file *ast.File, rawSnippet string) bool {
	if file == nil || strings.TrimSpace(rawSnippet) == "" {
		return false
	}
	fset := token.NewFileSet()
	snippet := strings.TrimSpace(rawSnippet)
	if !strings.HasPrefix(strings.ToLower(snippet), "type ") {
		snippet = "type " + snippet
	}
	parsed, err := parser.ParseFile(fset, "", "package dummy\n"+snippet, 0)
	if err != nil || len(parsed.Decls) == 0 {
		return false
	}
	for _, d := range parsed.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
			file.Decls = append(file.Decls, gd)
			return true
		}
	}
	return false
}

// AppendConstDecl parses a raw const declaration and appends it to the file AST.
func AppendConstDecl(file *ast.File, rawSnippet string) bool {
	if file == nil || strings.TrimSpace(rawSnippet) == "" {
		return false
	}
	fset := token.NewFileSet()
	snippet := strings.TrimSpace(rawSnippet)
	if !strings.HasPrefix(strings.ToLower(snippet), "const ") {
		snippet = "const " + snippet
	}
	parsed, err := parser.ParseFile(fset, "", "package dummy\n"+snippet, 0)
	if err != nil || len(parsed.Decls) == 0 {
		return false
	}
	for _, d := range parsed.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			file.Decls = append(file.Decls, gd)
			return true
		}
	}
	return false
}

// AppendVarDecl parses a raw variable declaration and appends it to the file AST.
func AppendVarDecl(file *ast.File, rawSnippet string) bool {
	if file == nil || strings.TrimSpace(rawSnippet) == "" {
		return false
	}
	fset := token.NewFileSet()
	snippet := strings.TrimSpace(rawSnippet)
	if !strings.HasPrefix(strings.ToLower(snippet), "var ") {
		snippet = "var " + snippet
	}
	parsed, err := parser.ParseFile(fset, "", "package dummy\n"+snippet, 0)
	if err != nil || len(parsed.Decls) == 0 {
		return false
	}
	for _, d := range parsed.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
			file.Decls = append(file.Decls, gd)
			return true
		}
	}
	return false
}

// AppendGenericDecl accepts a code snippet and appends whichever top-level declaration it contains.
func AppendGenericDecl(file *ast.File, rawSnippet string) bool {
	if file == nil || strings.TrimSpace(rawSnippet) == "" {
		return false
	}
	snippet := strings.TrimSpace(rawSnippet)
	if snippet == "" {
		return false
	}

	fset := token.NewFileSet()
	candidates := []string{snippet}
	if !strings.HasPrefix(strings.ToLower(snippet), "package ") {
		candidates = []string{"package dummy\n" + snippet, snippet}
	}

	for _, candidate := range candidates {
		parsed, err := parser.ParseFile(fset, "", candidate, 0)
		if err != nil {
			continue
		}

		for _, d := range parsed.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				continue
			}
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.PACKAGE {
				continue
			}
			file.Decls = append(file.Decls, d)
			return true
		}
	}

	candidate := snippet
	if !strings.Contains(strings.ToLower(candidate), "func ") {
		candidate = "func AutoAdded() {\n\t" + candidate + "\n}"
	}
	parsed, err := parser.ParseFile(fset, "", "package dummy\n"+candidate, 0)
	if err != nil {
		return false
	}
	for _, d := range parsed.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			continue
		}
		file.Decls = append(file.Decls, d)
		return true
	}
	return false
}
