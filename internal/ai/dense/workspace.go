package dense

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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
	Symbols   map[string]*SymbolRef // "pkg/db.Client" -> SymbolRef
	Files     map[string]*ast.File  // "/path/to/file.go" -> *ast.File
	Fsets     map[string]*token.FileSet
	Types     map[string]*types.Package
	TypesInfo map[string]*types.Info
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
		Symbols:   make(map[string]*SymbolRef),
		Files:     make(map[string]*ast.File),
		Fsets:     make(map[string]*token.FileSet),
		Types:     make(map[string]*types.Package),
		TypesInfo: make(map[string]*types.Info),
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
			// capture types info for semantic queries
			if pkg.Types != nil {
				graph.Types[pkgPath] = pkg.Types
			}
			if pkg.TypesInfo != nil {
				// associate types info with files
				for _, f := range pkg.GoFiles {
					graph.TypesInfo[f] = pkg.TypesInfo
				}
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

// FindObject attempts to locate a types.Object for a given bare name across
// all loaded packages in the workspace. Returns the first match found.
// RouteCodeIntent classifies a natural-language prompt into the command family
// the dense engine should execute for workspace-aware code generation and repair.
func RouteCodeIntent(prompt string) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return ""
	}

	switch {
	case strings.Contains(lower, "fix") || strings.Contains(lower, "compile") || strings.Contains(lower, "compilation") || strings.Contains(lower, "error"):
		return "cmd_fix"
	case strings.Contains(lower, "generate") || strings.Contains(lower, "handler") || strings.Contains(lower, "boilerplate"):
		return "cmd_generate"
	case strings.Contains(lower, "scaffold") || strings.Contains(lower, "restful") || strings.Contains(lower, "api") || strings.Contains(lower, "http"):
		return "cmd_scaffold"
	default:
		return "cmd_generate"
	}
}

func (wg *WorkspaceGraph) FindObject(name string) (types.Object, bool) {
	for _, pkg := range wg.Types {
		if pkg == nil || pkg.Scope() == nil {
			continue
		}
		if obj := pkg.Scope().Lookup(name); obj != nil {
			return obj, true
		}
	}
	return nil, false
}

// NamedType returns the types.Type for a named type (struct/interface) if present.
func (wg *WorkspaceGraph) NamedType(name string) (types.Type, bool) {
	if obj, ok := wg.FindObject(name); ok {
		if tn, ok := obj.(*types.TypeName); ok {
			return tn.Type(), true
		}
	}
	return nil, false
}

// MethodSetOf returns combined value and pointer receiver method selections for a named type.
func (wg *WorkspaceGraph) MethodSetOf(name string) ([]*types.Selection, bool) {
	t, ok := wg.NamedType(name)
	if !ok || t == nil {
		return nil, false
	}
	sel := make([]*types.Selection, 0)
	// value receiver methods
	vs := types.NewMethodSet(t)
	for i := 0; i < vs.Len(); i++ {
		sel = append(sel, vs.At(i))
	}
	// pointer receiver methods
	ps := types.NewMethodSet(types.NewPointer(t))
	for i := 0; i < ps.Len(); i++ {
		sel = append(sel, ps.At(i))
	}
	return sel, true
}

// ImplementsInterface reports whether the named type implements the requested interface.
func (wg *WorkspaceGraph) ImplementsInterface(namedType, ifaceName string) bool {
	t, ok := wg.NamedType(namedType)
	if !ok || t == nil {
		return false
	}
	obj, ok := wg.FindObject(ifaceName)
	if !ok || obj == nil {
		return false
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	return types.Implements(t, iface) || types.Implements(types.NewPointer(t), iface)
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
