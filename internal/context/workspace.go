package context

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Symbol represents a named declaration discovered during workspace scanning.
type Symbol struct {
	Name string
	Kind string
	File string
}

// WorkspaceContext holds the discovered Go files and symbol metadata for a project.
type WorkspaceContext struct {
	RootDir string
	Files   []string
	Symbols []Symbol
}

// ScanWorkspace parses all .go files in the project directory and records the
// declarations that are available for intent guessing and generation.
func ScanWorkspace(root string) (*WorkspaceContext, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	ctx := &WorkspaceContext{RootDir: absRoot}

	err = filepath.Walk(absRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor", "node_modules", ".idea", ".vscode":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		ctx.Files = append(ctx.Files, filepath.ToSlash(path))
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.TypeSpec:
				if x.Name != nil {
					ctx.Symbols = append(ctx.Symbols, Symbol{Name: x.Name.Name, Kind: "type", File: filepath.ToSlash(path)})
				}
			case *ast.FuncDecl:
				if x.Name != nil {
					ctx.Symbols = append(ctx.Symbols, Symbol{Name: x.Name.Name, Kind: "function", File: filepath.ToSlash(path)})
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ctx, nil
}
