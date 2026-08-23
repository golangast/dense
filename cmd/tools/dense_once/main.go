package main

import (
	"fmt"
	"os"

	"github.com/golangast/dense/internal/tools/oncefix"
)

func main() {
	dir := "."
	if wd, err := os.Getwd(); err == nil {
		dir = wd
	}
	fmt.Println("=== dense_once: running single suggestion pass ===")
	report, err := oncefix.RunOnce(dir, true, true)
	fmt.Print(report)
	if err != nil {
		os.Exit(1)
	}
}
