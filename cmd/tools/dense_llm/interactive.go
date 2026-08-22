package main

import (
	"bufio"
	"fmt"
	"go/token"
	"os"
	"strings"

	"github.com/golangast/dense/internal/ai/dense"
)

func persistWorkspaceMutation(targetFile string, graph *dense.WorkspaceGraph) error {
	if targetFile == "" {
		return fmt.Errorf("empty target file")
	}
	if graph == nil {
		return fmt.Errorf("workspace graph is nil")
	}
	fileAST, ok := graph.Files[targetFile]
	if !ok {
		return fmt.Errorf("target file %q is not indexed", targetFile)
	}
	fset := graph.Fsets[targetFile]
	if fset == nil {
		fset = token.NewFileSet()
	}
	return writeFormattedFile(targetFile, fset, fileAST)
}

func handleInteractivePrompt(dir, prompt string) (string, bool) {
	if msg, err := tryHandleImportAndUseWithDir(prompt, dir); err == nil && msg != "" {
		return msg, true
	}

	graph, err := dense.IndexWorkspace(dir)
	if err != nil {
		return fmt.Sprintf("⚠️  Workspace error: %v", err), false
	}

	slot := dense.ParseCodeAwarePrompt(prompt, graph)
	targetFile, ok := dense.RouteAndExecuteWorkspaceWithCodeAwareSlot(graph, "", slot)
	if !ok {
		return fmt.Sprintf("⚠️  Could not apply transformation for prompt: %q", prompt), false
	}
	if err := persistWorkspaceMutation(targetFile, graph); err != nil {
		return fmt.Sprintf("⚠️  Mutation was computed but not persisted for %s: %v", targetFile, err), false
	}
	return fmt.Sprintf("✅ Applied [%s] to %s", slot.Action, targetFile), true
}

// RunInteractiveREPL provides a simple persistent REPL for live edits.
func RunInteractiveREPL(dir string) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("⚡ golangast-dense REPL Mode (Type 'exit' or 'quit' to stop)")
	fmt.Println("────────────────────────────────────────────────────────────")

	for {
		fmt.Print("dense> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}

		prompt := strings.TrimSpace(line)
		if prompt == "" {
			continue
		}
		if prompt == "exit" || prompt == "quit" {
			fmt.Println("Bye!")
			break
		}

		msg, ok := handleInteractivePrompt(dir, prompt)
		if !ok {
			fmt.Println(msg)
			continue
		}
		fmt.Println(msg)
	}
}
