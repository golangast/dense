package cli

import (
	"fmt"
	"strings"

	"github.com/golangast/dense/internal/context"
)

// RunGenerateCommand generates boilerplate for a symbol found in the workspace.
func RunGenerateCommand(workDir, targetType string) error {
	ctx, err := context.ScanWorkspace(workDir)
	if err != nil {
		return err
	}

	var foundSymbol *context.Symbol
	for i := range ctx.Symbols {
		s := ctx.Symbols[i]
		if strings.EqualFold(s.Name, targetType) {
			foundSymbol = &s
			break
		}
	}
	if foundSymbol == nil {
		return fmt.Errorf("symbol %q not found in workspace AST", targetType)
	}

	fmt.Printf("⚙️ Generating boilerplate for %s (%s) found in %s...\n", foundSymbol.Name, foundSymbol.Kind, foundSymbol.File)
	return nil
}
