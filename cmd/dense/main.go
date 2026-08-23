package main

import (
	"fmt"
	"os"

	"github.com/golangast/dense/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "fix":
		workDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error resolving workdir: %v\n", err)
			os.Exit(1)
		}
		if err := cli.RunFixCommand(workDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error running fix: %v\n", err)
			os.Exit(1)
		}
	case "generate":
		if len(os.Args) < 3 {
			fmt.Println("Usage: dense generate <SymbolName>")
			os.Exit(1)
		}
		workDir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error resolving workdir: %v\n", err)
			os.Exit(1)
		}
		targetSymbol := os.Args[2]
		if err := cli.RunGenerateCommand(workDir, targetSymbol); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating code for %s: %v\n", targetSymbol, err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: dense <command> [arguments]")
	fmt.Println("Commands:")
	fmt.Println("  fix           Diagnose project and attempt automated fixes")
	fmt.Println("  generate      Generate boilerplate using workspace AST symbols")
}
