package dense

import (
	"fmt"
	"go/ast"
	"strings"
)

type InterfaceStub struct {
	Methods string
}

var KnownInterfaces = map[string]InterfaceStub{
	"Stringer": {
		Methods: "String() string {\n\treturn \"\"\n}",
	},
	"Handler": {
		Methods: "ServeHTTP(w http.ResponseWriter, r *http.Request) {\n\t\n}",
	},
	"Reader": {
		Methods: "Read(p []byte) (n int, err error) {\n\treturn 0, nil\n}",
	},
	"Writer": {
		Methods: "Write(p []byte) (n int, err error) {\n\treturn 0, nil\n}",
	},
}

// GenerateConstructor inspects a struct AST and builds New<Struct>(...) constructor
// as a raw function body string (without leading 'func '). The caller should
// pass the returned string to AppendFunctionDecl which will prepend 'func '.
func GenerateConstructorCode(file *ast.File, structName string) (string, bool) {
	if file == nil || structName == "" {
		return "", false
	}
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name == nil || typeSpec.Name.Name != structName {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			var params []string
			var assignments []string

			for _, field := range structType.Fields.List {
				if len(field.Names) == 0 {
					continue
				}
				fieldName := field.Names[0].Name
				paramName := strings.ToLower(fieldName[:1]) + fieldName[1:]

				// Format type string
				fieldType := "interface{}"
				if ident, ok := field.Type.(*ast.Ident); ok {
					fieldType = ident.Name
				}

				params = append(params, fmt.Sprintf("%s %s", paramName, fieldType))
				assignments = append(assignments, fmt.Sprintf("\t\t%s: %s,", fieldName, paramName))
			}

			paramStr := strings.Join(params, ", ")
			assignStr := strings.Join(assignments, "\n")

			code := fmt.Sprintf("New%s(%s) *%s {\n\treturn &%s{\n%s\n\t}\n}",
				structName, paramStr, structName, structName, assignStr)

			return code, true
		}
	}
	return "", false
}
