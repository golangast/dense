package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/golangast/dense/internal/knowledge"
)

func main() {
	phase := flag.Int("phase", 0, "study phase to review (1-4)")
	author := flag.String("author", "", "filter resources by author")
	category := flag.String("category", "", "filter resources by category")
	flag.Parse()

	catalog := knowledge.DefaultCatalog()
	if *author != "" {
		catalog.Resources = catalog.FilterByAuthor(*author)
	}
	if *category != "" {
		catalog.Resources = catalog.FilterByCategory(*category)
	}
	if *phase > 0 {
		catalog.Resources = catalog.ByPhase(*phase)
	}
	if len(catalog.Resources) == 0 {
		fmt.Println("No resources match that filter.")
		os.Exit(0)
	}

	rand.Seed(time.Now().UnixNano())
	res := catalog.Resources[rand.Intn(len(catalog.Resources))]
	fmt.Printf("Phase: %d\n", res.Phase)
	fmt.Printf("Difficulty: %s\n", res.Difficulty)
	fmt.Printf("Category: %s\n", res.Category)
	fmt.Printf("Author: %s\n", res.Author)
	fmt.Printf("Title: %s\n", res.Title)
	fmt.Printf("URL: %s\n", res.URL)
	fmt.Println()
	fmt.Println("Active recall prompt:")
	fmt.Println("Explain the core idea in 2-3 sentences before looking up the link.")
	fmt.Println("Example: what is the main mechanism behind this resource, and why is it useful in Go?")
}
