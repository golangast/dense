package oncefix

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"strings"

	"github.com/golangast/dense/internal/ai/dense"
	"github.com/golangast/dense/internal/generator"
)

// RunOnce diagnoses the project at dir, attempts auto-fixes, verifies with `go test`,
// and rolls back on failure. Returns a human-readable report and an error if the
// operation itself failed.
func RunOnce(dir string, autoApply, autoRestoreGit bool) (string, error) {
	var out strings.Builder
	diags, err := generator.DiagnoseProject(dir)
	if err != nil {
		out.WriteString(fmt.Sprintf("diagnose error: %v\n", err))
		return out.String(), err
	}
	if len(diags) == 0 {
		out.WriteString("No diagnostics found.\n")
		return out.String(), nil
	}

	backups := map[string][]byte{}
	appliedAny := false

	for _, d := range diags {
		out.WriteString(fmt.Sprintf("- %s:%d:%d %s\n", d.FilePath, d.Line, d.Column, d.Message))
		if _, ok := backups[d.FilePath]; !ok {
			if b, rerr := os.ReadFile(d.FilePath); rerr == nil {
				backups[d.FilePath] = b
			} else {
				backups[d.FilePath] = nil
			}
		}
		out.WriteString(fmt.Sprintf("Applying auto-fix to %s...\n", d.FilePath))
		if generator.AutoFixFile(d) {
			out.WriteString(fmt.Sprintf("Auto-fix applied to %s\n", d.FilePath))
			appliedAny = true
			continue
		}

		// fallback: try a code-aware REPLACE of NewServer when AutoFix fails
		graph, gerr := dense.IndexWorkspace(dir)
		if gerr == nil {
			slot := dense.CodeAwareSlot{}
			slot.Action = "REPLACE"
			slot.TargetSymbol = "NewServer"
			slot.PayloadCode = "func NewServer(addr string) *Server { return &Server{Addr: addr} }"
			if target, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "", slot); ok {
				fset := graph.Fsets[target]
				node := graph.Files[target]
				var buf bytes.Buffer
				if err := format.Node(&buf, fset, node); err == nil {
					if werr := os.WriteFile(target, buf.Bytes(), 0644); werr == nil {
						out.WriteString(fmt.Sprintf("Code-aware replacement applied to %s\n", target))
						appliedAny = true
						continue
					}
				}
			}
		}

		out.WriteString(fmt.Sprintf("Auto-fix failed for %s\n", d.FilePath))
	}

	if !appliedAny {
		out.WriteString("No changes applied.\n")
		return out.String(), nil
	}

	out.WriteString("Verifying changes by running 'go test ./...'...\n")
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	var cb bytes.Buffer
	cmd.Stdout = &cb
	cmd.Stderr = &cb
	err = cmd.Run()
	out.WriteString(cb.String())
	if err != nil {
		out.WriteString("Verification failed; rolling back.\n")
		for fp, content := range backups {
			if content == nil {
				if autoRestoreGit {
					exec.Command("git", "checkout", "--", fp).Run()
					out.WriteString(fmt.Sprintf("Restored %s from git\n", fp))
					continue
				}
				continue
			}
			_ = os.WriteFile(fp, content, 0644)
			out.WriteString(fmt.Sprintf("Restored %s\n", fp))
		}
		return out.String(), fmt.Errorf("verification failed: %w", err)
	}

	out.WriteString("Verification passed. Changes kept.\n")
	return out.String(), nil
}
