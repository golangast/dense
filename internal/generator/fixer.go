package generator

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// GoError describes a single compiler or test error recognized from Go output.
type GoError struct {
	FilePath string
	Line     string
	Column   string
	Message  string
}

// DiagnoseProject runs the Go toolchain against a project and captures compiler errors.
func DiagnoseProject(dir string) ([]GoError, error) {
	if dir == "" {
		dir = "."
	}

	out, _ := runGoCommand(dir, "go", "test", "./...")
	if parsed := parseGoErrors(out); len(parsed) > 0 {
		return parsed, nil
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	if len(files) == 0 {
		return nil, nil
	}
	var all []GoError
	for _, file := range files {
		out, _ = runGoCommand(dir, "go", "run", file)
		if parsed := parseGoErrors(out); len(parsed) > 0 {
			all = append(all, parsed...)
		}
	}
	if len(all) > 0 {
		return all, nil
	}
	return nil, nil
}

func runGoCommand(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func parseGoErrors(output string) []GoError {
	re := regexp.MustCompile(`(?m)^(.+?\.go):(\d+):(\d+):\s+(.+)$`)
	matches := re.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]GoError, 0, len(matches))
	for _, m := range matches {
		if len(m) < 5 {
			continue
		}
		out = append(out, GoError{
			FilePath: m[1],
			Line:     m[2],
			Column:   m[3],
			Message:  m[4],
		})
	}
	return out
}

// TryAutoFixFile attempts a minimal safe fix for a broken Go file based on the compiler error.
func TryAutoFixFile(filePath, message string) (bool, error) {
	if strings.TrimSpace(filePath) == "" {
		return false, fmt.Errorf("empty file path")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return false, err
	}
	updated := string(content)
	trimmed := strings.TrimSpace(updated)
	if !strings.HasPrefix(trimmed, "package ") {
		updated = "package main\n\n" + updated
	}
	if strings.Contains(message, "undefined: fmt") && strings.Contains(updated, "fmt.") && !strings.Contains(updated, "import \"fmt\"") {
		updated = strings.Replace(updated, "package main\n\n", "package main\n\nimport \"fmt\"\n\n", 1)
	}
	if strings.Contains(message, "undefined:") {
		for _, match := range regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`).FindAllStringSubmatch(message, -1) {
			if len(match) < 2 {
				continue
			}
			ident := match[1]
			if ident == "fmt" {
				if !strings.Contains(updated, "import \"fmt\"") {
					updated = strings.Replace(updated, "package main\n\n", "package main\n\nimport \"fmt\"\n\n", 1)
				}
				continue
			}
			if !strings.Contains(updated, "var "+ident) && !strings.Contains(updated, ident+" ") {
				updated = strings.Replace(updated, "func main()", "var "+ident+" = \"\"\n\nfunc main()", 1)
			}
		}
	}
	if updated == string(content) {
		return false, nil
	}
	if err := os.WriteFile(filePath, []byte(updated), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// ExampleIntentCommand is a small helper used to route generated commands in tests.
func ExampleIntentCommand(prompt string) string {
	switch {
	case len(prompt) == 0:
		return ""
	case regexp.MustCompile(`(?i)\bfix\b|\bcompilation\b|\berror\b`).MatchString(prompt):
		return "cmd_fix"
	case regexp.MustCompile(`(?i)\bgenerate\b|\bhandler\b|\bscaffold\b`).MatchString(prompt):
		return "cmd_generate"
	case regexp.MustCompile(`(?i)\brestful\b|\bapi\b|\bhttp\b`).MatchString(prompt):
		return "cmd_scaffold"
	default:
		return "cmd_generate"
	}
}

var _ = fmt.Sprintf
