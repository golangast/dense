package dense

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/tools/go/packages"
)

// SymbolLocation stores the workspace lookup metadata for a symbol.
type SymbolLocation struct {
	PkgPath  string
	FileName string
	TypeName string
	Node     ast.Node
}

// ProjectIndex holds a workspace-wide view of Go packages and symbols.
type ProjectIndex struct {
	Packages map[string]*packages.Package
	Symbols  map[string]SymbolLocation
	RootDir  string
}

// GoIdiomFeatures captures granular Go syntax patterns used in a function.
type GoIdiomFeatures struct {
	HasGenerics             bool
	HasChannels             bool
	HasPointers             bool
	IsVariadic              bool
	HasStructComposition    bool
	HasSendOnlyChannel      bool
	HasRecvOnlyChannel      bool
	HasBidirectionalChannel bool
}

// AnalyzeFuncIdioms extracts deep Go language constructs from a function declaration.
func AnalyzeFuncIdioms(fn *ast.FuncDecl) GoIdiomFeatures {
	var feats GoIdiomFeatures
	if fn == nil || fn.Type == nil {
		return feats
	}

	if fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) > 0 {
		feats.HasGenerics = true
	}

	inspectFieldList := func(list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, field := range list.List {
			if field == nil || field.Type == nil {
				continue
			}
			ast.Inspect(field.Type, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.Ellipsis:
					feats.IsVariadic = true
				case *ast.ChanType:
					feats.HasChannels = true
					switch node.Dir {
					case ast.SEND:
						feats.HasSendOnlyChannel = true
					case ast.RECV:
						feats.HasRecvOnlyChannel = true
					default:
						feats.HasBidirectionalChannel = true
					}
				case *ast.StarExpr:
					feats.HasPointers = true
				case *ast.StructType:
					for _, embeddedField := range node.Fields.List {
						if len(embeddedField.Names) == 0 {
							feats.HasStructComposition = true
							break
						}
					}
				}
				return true
			})
		}
	}

	inspectFieldList(fn.Type.Params)
	inspectFieldList(fn.Type.Results)
	return feats
}

// LoadProjectIndex scans the root module or workspace and indexes all loaded packages.
func LoadProjectIndex(rootDir string) (*ProjectIndex, error) {
	if strings.TrimSpace(rootDir) == "" {
		rootDir = "."
	}
	info, err := os.Stat(rootDir)
	if err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("rootDir %q is not a directory", rootDir)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedSyntax |
			packages.NeedModule,
		Dir: rootDir,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}

	index := &ProjectIndex{
		Packages: make(map[string]*packages.Package),
		Symbols:  make(map[string]SymbolLocation),
		RootDir:  rootDir,
	}

	for _, pkg := range pkgs {
		if pkg == nil || pkg.PkgPath == "" {
			continue
		}
		index.Packages[pkg.PkgPath] = pkg
		for _, file := range pkg.Syntax {
			path := ""
			if pkg.Fset != nil {
				for _, f := range pkg.CompiledGoFiles {
					if strings.HasSuffix(f, filepath.Base(pkg.Fset.File(file.Pos()).Name())) {
						path = f
						break
					}
				}
			}
			if path == "" {
				path = filepath.Base(pkg.GoFiles[0])
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch decl := n.(type) {
				case *ast.FuncDecl:
					symbolName := pkg.PkgPath + "." + decl.Name.Name
					index.Symbols[symbolName] = SymbolLocation{PkgPath: pkg.PkgPath, FileName: path, TypeName: "func", Node: decl}
				case *ast.TypeSpec:
					symbolName := pkg.PkgPath + "." + decl.Name.Name
					index.Symbols[symbolName] = SymbolLocation{PkgPath: pkg.PkgPath, FileName: path, TypeName: "type", Node: decl}
				}
				return true
			})
		}
	}
	return index, nil
}

// GlobalContextFeatures returns compact project-wide context metrics for classification.
func GlobalContextFeatures(index *ProjectIndex, currentPkg string) []float32 {
	features := make([]float32, 8)
	if index == nil {
		return features
	}
	features[0] = float32(len(index.Packages))
	features[1] = float32(len(index.Symbols))
	if pkg, ok := index.Packages[currentPkg]; ok {
		features[2] = float32(len(pkg.Imports))
		if pkg.Types != nil {
			features[3] = float32(len(pkg.Types.Scope().Names()))
		}
	}
	if currentPkg != "" {
		for pkgPath, sym := range index.Symbols {
			if strings.HasPrefix(pkgPath, currentPkg+".") {
				features[4] += 1
			}
			if sym.PkgPath == currentPkg {
				features[5] += 1
			}
		}
	}
	return features
}

// ValidateWorkspace reports the first package-level error in the project index.
func ValidateWorkspace(index *ProjectIndex) error {
	if index == nil {
		return nil
	}
	for pkgPath, pkg := range index.Packages {
		if pkg == nil {
			continue
		}
		if len(pkg.Errors) > 0 {
			return fmt.Errorf("package %s has type errors: %v", pkgPath, pkg.Errors[0])
		}
	}
	return nil
}

// ReloadFile re-indexes a single Go source file without rebuilding the entire workspace.
func (idx *ProjectIndex) ReloadFile(path string) error {
	if idx == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse file %s: %w", path, err)
	}
	if idx.Symbols == nil {
		idx.Symbols = make(map[string]SymbolLocation)
	}
	if idx.Packages == nil {
		idx.Packages = make(map[string]*packages.Package)
	}
	pkgPath := ""
	if file.Name != nil && file.Name.Name != "" {
		pkgPath = file.Name.Name
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			key := pkgPath + "." + d.Name.Name
			idx.Symbols[key] = SymbolLocation{PkgPath: pkgPath, FileName: path, TypeName: "func", Node: d}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					key := pkgPath + "." + ts.Name.Name
					idx.Symbols[key] = SymbolLocation{PkgPath: pkgPath, FileName: path, TypeName: "type", Node: ts}
				}
			}
		}
	}
	return nil
}

// WatchAndInvalidate watches a directory for Go file changes and refreshes the affected file in memory.
func (idx *ProjectIndex) WatchAndInvalidate(watchDir string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 && strings.HasSuffix(event.Name, ".go") {
					if err := idx.ReloadFile(event.Name); err != nil {
						log.Println("reload file:", err)
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("Watcher error:", err)
			}
		}
	}()
	return watcher.Add(watchDir)
}

// PrintSummary prints a compact workspace view of indexed packages.
func (idx *ProjectIndex) PrintSummary() {
	if idx == nil {
		return
	}
	fmt.Printf("Indexed %d packages across workspace:\n", len(idx.Packages))
	for pkgPath, pkg := range idx.Packages {
		typesAndFuncs := 0
		for _, sym := range idx.Symbols {
			if sym.PkgPath == pkgPath {
				typesAndFuncs++
			}
		}
		fmt.Printf(" - %s (%d files, %d types/funcs)\n", pkgPath, len(pkg.Syntax), typesAndFuncs)
	}
}

// ResolveCrossPackageIntent is a lightweight workspace-aware symbol lookup hook.
func ResolveCrossPackageIntent(index *ProjectIndex, targetPkg string, interfaceName string) error {
	if index == nil {
		return nil
	}
	if targetPkg == "" {
		return fmt.Errorf("target package is empty")
	}
	key := targetPkg + "." + interfaceName
	loc, ok := index.Symbols[key]
	if !ok {
		return fmt.Errorf("symbol %s not found in workspace", key)
	}
	if loc.TypeName == "type" {
		if _, ok := loc.Node.(*ast.TypeSpec); !ok {
			return fmt.Errorf("symbol %s is not a type declaration", key)
		}
	}
	_ = token.NoPos
	return nil
}
