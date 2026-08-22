package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/golangast/dense/internal/ai/dense"
)

// runNativeTUI provides a lightweight split-pane terminal workflow without any
// external UI dependency. It mimics the requested side-by-side preview, prompt
// entry, and Markov suggestion bar using plain ANSI/terminal output.
func runNativeTUI(model *dense.DenseModel, examples []dense.CommandExample, targetFile string) error {
	fmt.Println("dense native terminal interface")
	fmt.Println("Type a prompt below, or /help for commands.")
	fmt.Println("Commands: /apply, /file <path>, /suggest, /quit")

	currentFile := targetFile
	if currentFile == "" {
		currentFile = "dense_generated.go"
	}
	if err := ensureGoTargetFile(currentFile, ""); err != nil {
		return err
	}

	var pendingCode string

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("\n[%s] > ", filepath.Base(currentFile))
		line, err := reader.ReadString('\n')
		if err != nil {
			if err.Error() == "EOF" {
				fmt.Println()
				return nil
			}
			return err
		}
		prompt := strings.TrimSpace(line)
		if prompt == "" {
			continue
		}
		if strings.EqualFold(prompt, "/quit") || strings.EqualFold(prompt, "/exit") {
			fmt.Println("Bye.")
			return nil
		}
		if strings.EqualFold(prompt, "/help") {
			fmt.Println("/apply      preview the file change and write it to disk")
			fmt.Println("/file PATH  set/edit the target file")
			fmt.Println("/suggest    list the next action suggestions")
			fmt.Println("/quit       exit")
			continue
		}
		if strings.HasPrefix(strings.ToLower(prompt), "/file") {
			parts := strings.Fields(prompt)
			if len(parts) < 2 {
				fmt.Println("usage: /file <path>")
				continue
			}
			currentFile = strings.Join(parts[1:], " ")
			if err := ensureGoTargetFile(currentFile, prompt); err != nil {
				fmt.Printf("could not prepare target file: %v\n", err)
				continue
			}
			fmt.Printf("target file: %s\n", currentFile)
			continue
		}
		if strings.EqualFold(prompt, "/suggest") {
			fmt.Println(suggestionBar([]string{"add json tags", "wrap errors", "generate constructor", "add unit test", "add import"}))
			continue
		}
		if strings.EqualFold(prompt, "/apply") {
			if pendingCode == "" {
				fmt.Println("no pending change to apply")
				continue
			}
			msg, err := nativeTUIApplyPreview(currentFile, pendingCode)
			if err != nil {
				fmt.Printf("apply failed: %v\n", err)
				continue
			}
			fmt.Println(msg)
			pendingCode = ""
			continue
		}

		intent := predictIntent(prompt, nil, model, examples)
		if intent.Action == "" {
			fmt.Println("no direct intent matched; using generic preview")
			intent = Intent{Action: "ADD_FUNC", Name: "Generated"}
		}
		code, err := renderIntentToCode(intent)
		if err != nil {
			fmt.Printf("render error: %v\n", err)
			continue
		}
		pendingCode = code
		original, err := os.ReadFile(currentFile)
		if err != nil {
			original = []byte("package main\n")
		}
		fmt.Println(renderSplitPane(string(original), code, prompt, suggestionBar(predictedActionsForPrompt(prompt))))
		fmt.Println("\nType /apply to write this change to disk.")
	}
}

func nativeTUIApplyPreview(filePath, code string) (string, error) {
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return "", fmt.Errorf("no code to apply")
	}
	if err := ensureGoTargetFile(filePath, ""); err != nil {
		return "", err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	updated, msg, err := applyCodeViaAST(filePath, string(content), code)
	if err != nil || !updated {
		if err == nil {
			err = fmt.Errorf("no AST change applied")
		}
		return "", err
	}
	if msg == "" {
		msg = "applied preview to file"
	}
	return fmt.Sprintf("✅ Applied to %s: %s", filePath, msg), nil
}

func renderSplitPane(original, generated, prompt, suggestions string) string {
	left := trimToWidth(formatCodeBlock(original), 42)
	right := trimToWidth(formatCodeBlock(generated), 42)
	var sb strings.Builder
	sb.WriteString("\n=== prompt: " + prompt + " ===\n")
	sb.WriteString("left: original code                | right: preview\n")
	for i := 0; i < maxLen(left, right); i++ {
		l := ""
		r := ""
		if i < len(left) {
			l = string(left[i])
		}
		if i < len(right) {
			r = string(right[i])
		}
		sb.WriteString(l)
		if i < len(left) {
			sb.WriteString(" ")
		}
		sb.WriteString(" | ")
		if i < len(right) {
			sb.WriteString(r)
		}
		sb.WriteString("\n")
	}
	if strings.TrimSpace(suggestions) != "" {
		sb.WriteString("\n=== suggestions ===\n")
		sb.WriteString(suggestions)
	}
	return sb.String()
}

func diffPreview(before, after string) string {
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	maxLen := maxInt(len(beforeLines), len(afterLines))
	var sb strings.Builder
	changed := false
	for i := 0; i < maxLen; i++ {
		b := ""
		a := ""
		if i < len(beforeLines) {
			b = beforeLines[i]
		}
		if i < len(afterLines) {
			a = afterLines[i]
		}
		if b == a {
			continue
		}
		changed = true
		if b != "" {
			sb.WriteString("- " + b + "\n")
		}
		if a != "" {
			sb.WriteString("+ " + a + "\n")
		}
	}
	if !changed {
		return "(no changes)"
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

func suggestionBar(items []string) string {
	if len(items) == 0 {
		return "[no suggestions]"
	}
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
		if items[i] == "" {
			continue
		}
	}
	return "[1] " + strings.Join(items, "   |   ")
}

func predictedActionsForPrompt(prompt string) []string {
	lower := strings.ToLower(prompt)
	switch {
	case strings.Contains(lower, "json"):
		return []string{"add json tags", "generate constructor", "add unit test"}
	case strings.Contains(lower, "error"):
		return []string{"wrap errors", "add nil check", "add unit test"}
	case strings.Contains(lower, "test"):
		return []string{"add unit test", "generate constructor", "run go test"}
	default:
		return []string{"add json tags", "wrap errors", "generate constructor"}
	}
}

func formatCodeBlock(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, func(r rune) bool { return unicode.IsSpace(r) })
	}
	return strings.Join(lines, "\n")
}

func trimToWidth(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if len(line) > width {
			lines[i] = line[:width]
		}
	}
	return strings.Join(lines, "\n")
}

func maxLen(a, b string) int {
	if len(a) > len(b) {
		return len(a)
	}
	return len(b)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
