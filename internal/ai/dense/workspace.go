package dense

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// SymbolRef describes a single named declaration found anywhere in the workspace.
type SymbolRef struct {
	Name     string   // bare identifier, e.g. "Client"
	Kind     string   // "func", "struct", "interface", "var", "const", "type"
	Package  string   // full import path, e.g. "github.com/golangast/dense/internal/db"
	PkgPath  string   // alias for Package to match the workspace-indexing API
	FilePath string   // absolute path to the source file
	Node     ast.Node // live AST node for direct mutation
	Fset     *token.FileSet
}

// WorkspaceGraph is an in-memory index of every named symbol across all packages
// in the project root, keyed by "<pkgPath>.<Name>".
type WorkspaceGraph struct {
	Symbols map[string]*SymbolRef // "pkg/db.Client" -> SymbolRef
	Files   map[string]*ast.File  // "/path/to/file.go" -> *ast.File
	Fsets   map[string]*token.FileSet
}

// IndexWorkspace recursively parses every Go package under rootPath using
// go/packages and builds a full cross-file symbol index.
func IndexWorkspace(rootPath string) (*WorkspaceGraph, error) {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve root path: %w", err)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedImports,
		Dir:  rootPath,
		Fset: token.NewFileSet(),
	}

	pkgs, loadErr := packages.Load(cfg, "./...")
	graph := &WorkspaceGraph{
		Symbols: make(map[string]*SymbolRef),
		Files:   make(map[string]*ast.File),
		Fsets:   make(map[string]*token.FileSet),
	}

	if loadErr == nil && len(pkgs) > 0 {
		for _, pkg := range pkgs {
			if pkg == nil || len(pkg.GoFiles) == 0 {
				continue
			}
			pkgPath := pkg.PkgPath
			if pkgPath == "" {
				pkgPath = filepath.ToSlash(strings.TrimPrefix(rootPath, filepath.Dir(rootPath)))
			}
			for i, filePath := range pkg.GoFiles {
				if i >= len(pkg.Syntax) {
					fileAST, parseErr := parser.ParseFile(token.NewFileSet(), filePath, nil, parser.ParseComments)
					if parseErr != nil {
						continue
					}
					graph.Files[filePath] = fileAST
					graph.Fsets[filePath] = token.NewFileSet()
					indexFileSymbols(graph, pkgPath, filePath, fileAST)
					continue
				}
				fileAST := pkg.Syntax[i]
				graph.Files[filePath] = fileAST
				graph.Fsets[filePath] = cfg.Fset
				indexFileSymbols(graph, pkgPath, filePath, fileAST)
			}
		}
	}

	if len(graph.Symbols) == 0 || len(graph.Files) == 0 {
		if err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules" || d.Name() == ".dense" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" {
				return nil
			}
			fileAST, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if parseErr != nil {
				return nil
			}
			absPath, _ := filepath.Abs(path)
			graph.Files[absPath] = fileAST
			graph.Fsets[absPath] = token.NewFileSet()
			indexFileSymbols(graph, filepath.ToSlash(strings.TrimPrefix(rootPath, filepath.Dir(rootPath))), absPath, fileAST)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walk workspace: %w", err)
		}
	}

	if len(graph.Symbols) == 0 && loadErr != nil {
		return nil, fmt.Errorf("failed to load workspace: %w", loadErr)
	}

	return graph, nil
}

func indexFileSymbols(graph *WorkspaceGraph, pkgPath, filePath string, fileAST *ast.File) {
	for _, decl := range fileAST.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			ref := &SymbolRef{
				Name:     d.Name.Name,
				Kind:     "func",
				Package:  pkgPath,
				PkgPath:  pkgPath,
				FilePath: filePath,
				Node:     d,
				Fset:     graph.Fsets[filePath],
			}
			graph.Symbols[d.Name.Name] = ref
			if pkgPath != "" {
				graph.Symbols[pkgPath+"."+d.Name.Name] = ref
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name == nil {
						continue
					}
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					ref := &SymbolRef{
						Name:     s.Name.Name,
						Kind:     kind,
						Package:  pkgPath,
						PkgPath:  pkgPath,
						FilePath: filePath,
						Node:     s,
						Fset:     graph.Fsets[filePath],
					}
					graph.Symbols[s.Name.Name] = ref
					if pkgPath != "" {
						graph.Symbols[pkgPath+"."+s.Name.Name] = ref
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						kind := "var"
						if d.Tok == token.CONST {
							kind = "const"
						}
						ref := &SymbolRef{
							Name:     name.Name,
							Kind:     kind,
							Package:  pkgPath,
							PkgPath:  pkgPath,
							FilePath: filePath,
							Node:     s,
							Fset:     graph.Fsets[filePath],
						}
						graph.Symbols[name.Name] = ref
						if pkgPath != "" {
							graph.Symbols[pkgPath+"."+name.Name] = ref
						}
					}
				}
			}
		}
	}
}

// FindSymbol locates a named symbol anywhere across the indexed workspace.
// It first tries an exact full-key match, then a bare-name scan.
func (wg *WorkspaceGraph) FindSymbol(name string) (*SymbolRef, bool) {
	// Exact key match (e.g. "github.com/golangast/dense/jim.Jake")
	if sym, ok := wg.Symbols[name]; ok {
		return sym, true
	}
	// Bare-name search across all packages.
	for _, sym := range wg.Symbols {
		if sym.Name == name {
			return sym, true
		}
	}
	return nil, false
}

// FindSymbolByKind returns the first symbol matching name AND kind.
func (wg *WorkspaceGraph) FindSymbolByKind(name, kind string) (*SymbolRef, bool) {
	for _, sym := range wg.Symbols {
		if sym.Name == name && sym.Kind == kind {
			return sym, true
		}
	}
	return nil, false
}

// PackageSymbols returns all symbols belonging to a given package import path.
func (wg *WorkspaceGraph) PackageSymbols(pkgPath string) []*SymbolRef {
	var out []*SymbolRef
	for _, sym := range wg.Symbols {
		if sym.Package == pkgPath {
			out = append(out, sym)
		}
	}
	return out
}
