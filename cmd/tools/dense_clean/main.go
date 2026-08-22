package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/golangast/dense/internal/ai/dense"
)

func main() {
	csvPath := flag.String("csv", "data/training/command_examples.csv", "path to the CSV training file")
	protoPath := flag.String("pb", "data/training/command_examples.pb", "path to the protobuf training file")
	maxPerClass := flag.Int("max-per-class", 200, "cap for each class during balancing")
	writeCSV := flag.Bool("write-csv", true, "write cleaned data back to CSV")
	writeProto := flag.Bool("write-pb", true, "write cleaned data back to protobuf")
	flag.Parse()

	examples, err := dense.LoadCommandExamplesFromCSV(*csvPath)
	if err != nil {
		log.Printf("load csv: %v", err)
		examples = nil
	}
	if len(examples) == 0 {
		log.Printf("no csv examples found at %s; trying protobuf fallback", *csvPath)
		examples, err = dense.LoadCommandExamplesFromProto(*protoPath)
		if err != nil {
			log.Fatalf("load training data: %v", err)
		}
	}

	cleaned := dense.CleanDataset(examples, *maxPerClass)
	log.Printf("Cleaned dataset: pruned from %d to %d unique prompts", len(examples), len(cleaned))

	if *writeCSV {
		if err := os.MkdirAll(filepath.Dir(*csvPath), 0755); err != nil {
			log.Fatalf("create csv dir: %v", err)
		}
		if err := dense.SaveCSV(*csvPath, cleaned); err != nil {
			log.Fatalf("save csv: %v", err)
		}
		fmt.Printf("CSV saved to %s\n", *csvPath)
	}
	if *writeProto {
		if err := os.MkdirAll(filepath.Dir(*protoPath), 0755); err != nil {
			log.Fatalf("create proto dir: %v", err)
		}
		if err := dense.SaveProto(*protoPath, cleaned); err != nil {
			log.Fatalf("save proto: %v", err)
		}
		fmt.Printf("Proto saved to %s\n", *protoPath)
	}

	counts := make(map[string]int)
	for _, ex := range cleaned {
		label := strings.TrimSpace(ex.Type)
		if label == "" {
			label = "social"
		}
		counts[label]++
	}
	fmt.Printf("Label summary: %v\n", counts)
}
