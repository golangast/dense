package main

import (
	"fmt"
	"log"
	"sort"

	"github.com/golangast/dense/internal/ai/dense"
)

func main() {
	model, err := dense.LoadGob("data/models/dense/model.gob")
	if err != nil {
		log.Fatalf("Failed to load model: %v", err)
	}

	examples, err := dense.LoadCommandExamplesFromCSV("data/training/command_examples.csv")
	if err != nil {
		log.Fatalf("Failed to load CSV: %v", err)
	}

	wanted := map[string]bool{
		"social":      true,
		"code_update": true,
		"file_create": true,
	}
	var correct, total int
	confusionMatrix := make(map[string]map[string]int)

	for _, example := range examples {
		if !wanted[example.Type] {
			continue
		}

		input := dense.BagOfWords(example.Prompt, dense.CommandVocab)
		preds := model.Predict([][]float32{input})
		if len(preds) == 0 {
			log.Fatalf("model produced no prediction for prompt: %q", example.Prompt)
		}

		predicted := dense.CommandLabels[preds[0]]
		if confusionMatrix[example.Type] == nil {
			confusionMatrix[example.Type] = make(map[string]int)
		}
		confusionMatrix[example.Type][predicted]++

		if predicted == example.Type {
			correct++
		}
		total++
	}

	if total == 0 {
		log.Fatal("no validation samples available for the target labels")
	}

	accuracy := float64(correct) / float64(total) * 100.0
	fmt.Printf("Accuracy: %.2f%% (%d/%d)\n\n", accuracy, correct, total)
	fmt.Println("Confusion Matrix (Rows: Actual, Cols: Predicted):")
	actuals := make([]string, 0, len(confusionMatrix))
	for actual := range confusionMatrix {
		actuals = append(actuals, actual)
	}
	sort.Strings(actuals)
	for _, actual := range actuals {
		preds := confusionMatrix[actual]
		predList := make([]string, 0, len(preds))
		for pred := range preds {
			predList = append(predList, pred)
		}
		sort.Strings(predList)
		fmt.Printf("Actual [%s]: ", actual)
		for i, pred := range predList {
			if i > 0 {
				fmt.Print(" ")
			}
			fmt.Printf("%s=%d", pred, preds[pred])
		}
		fmt.Println()
	}
}
