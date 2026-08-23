package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golangast/dense/internal/context"
	"github.com/golangast/dense/internal/generator"
)

// RunFixCommand runs a compiler diagnosis loop and repairs common issues in place.
func RunFixCommand(workDir string) error {
	fmt.Println("🔍 Diagnosing project for compiler errors...")

	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		errs, err := generator.DiagnoseProject(workDir)
		if err != nil {
			return fmt.Errorf("failed to run diagnosis: %w", err)
		}
		if len(errs) == 0 {
			fmt.Println("✅ All compilation errors fixed successfully!")
			return nil
		}

		fmt.Printf("⚠️ Attempt %d/%d: Found %d error(s). Repairing...\n", i+1, maxRetries, len(errs))
		anyFixed := false
		for _, e := range errs {
			fmt.Printf(" ↳ Auto-fixing %s:%d: %s\n", e.FilePath, e.Line, e.Message)
			if generator.AutoFixFile(e) {
				anyFixed = true
			}
		}
		if !anyFixed {
			fmt.Println("❌ Unable to resolve remaining errors automatically.")
			break
		}
	}
	return nil
}

// BuildFixContext reads the broken file and nearby function body before a patch is applied.
func BuildFixContext(workDir, filePath string, lineNumber int) (string, error) {
	absFile, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(absFile)
	if err != nil {
		return "", err
	}

	ctx, scanErr := context.ScanWorkspace(workDir)
	if scanErr != nil {
		return "", scanErr
	}
	for _, sym := range ctx.Symbols {
		if sym.File == filepath.ToSlash(absFile) {
			_ = sym
		}
	}

	lines := strings.Split(string(content), "\n")
	start := lineNumber - 3
	if start < 1 {
		start = 1
	}
	end := lineNumber + 5
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}
