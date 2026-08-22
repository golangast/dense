package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golangast/dense/internal/context"
	"github.com/golangast/dense/internal/generator"
)

// RunFixCommand runs the compiler diagnosis loop and applies the smallest safe fix.
func RunFixCommand(workDir string) error {
	fmt.Println("🔍 Diagnosing project for compiler errors...")

	errs, err := generator.DiagnoseProject(workDir)
	if err != nil {
		return fmt.Errorf("failed to run diagnosis: %w", err)
	}
	if len(errs) == 0 {
		fmt.Println("✅ No compilation errors detected!")
		return nil
	}

	fmt.Printf("⚠️ Found %d error(s). Attempting auto-fix...\n", len(errs))
	changed := false
	for _, e := range errs {
		fmt.Printf(" ↳ Fix target: %s:%s - %s\n", e.FilePath, e.Line, e.Message)
		patched, patchErr := generator.TryAutoFixFile(e.FilePath, e.Message)
		if patchErr != nil {
			fmt.Printf("   - patch failed: %v\n", patchErr)
			continue
		}
		if patched {
			changed = true
		}
	}

	if !changed {
		fmt.Println("No safe automatic patch was applied.")
		return nil
	}

	fmt.Println("🧪 Re-running compiler diagnostics...")
	recheck, recheckErr := generator.DiagnoseProject(workDir)
	if recheckErr != nil {
		return fmt.Errorf("failed to validate auto-fix: %w", recheckErr)
	}
	if len(recheck) == 0 {
		fmt.Println("✅ Auto-fix resolved all compilation errors.")
		return nil
	}

	fmt.Printf("⚠️ %d error(s) remain after auto-fix.\n", len(recheck))
	for _, e := range recheck {
		fmt.Printf(" ↳ Remaining: %s:%s - %s\n", e.FilePath, e.Line, e.Message)
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
