package dense

import (
	"encoding/csv"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"strings"

	trainingpb "github.com/golangast/dense/internal/ai/training"
)

// LoadCommandExamplesFromCSV reads CommandExample records from a CSV file.
func LoadModel(path string) (*DenseModel, error) {
	return LoadGob(path)
}

func LoadCSV(path string) ([]CommandExample, error) {
	return LoadCommandExamplesFromCSV(path)
}

func LoadCommandExamplesFromCSV(path string) ([]CommandExample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	var records [][]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, fmt.Errorf("read csv: %w", err)
			}
			backtickRecords, backtickErr := parseBacktickCSV(string(data))
			if backtickErr != nil {
				return nil, fmt.Errorf("parse csv: %w", err)
			}
			records = backtickRecords
			break
		}
		records = append(records, record)
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("empty csv file")
	}
	records = records[1:]

	var examples []CommandExample
	for i, rec := range records {
		if len(rec) < 4 {
			return nil, fmt.Errorf("record %d: csv record has %d fields, want 4: %v", i+2, len(rec), rec)
		}
		examples = append(examples, CommandExample{
			Type:      strings.TrimSpace(rec[0]),
			Prompt:    strings.TrimSpace(rec[1]),
			Response:  strings.TrimSpace(rec[2]),
			CodeAfter: strings.TrimSpace(rec[3]),
		})
	}
	return examples, nil
}

func parseBacktickCSV(content string) ([][]string, error) {
	var records [][]string
	var fields []string
	var current strings.Builder
	inBacktick := false

	flushField := func() {
		fields = append(fields, current.String())
		current.Reset()
	}
	flushRecord := func() {
		flushField()
		records = append(records, fields)
		fields = nil
	}

	for i := 0; i < len(content); i++ {
		c := content[i]
		switch {
		case c == '`':
			inBacktick = !inBacktick
		case c == ',' && !inBacktick:
			flushField()
		case c == '\n' && !inBacktick:
			flushRecord()
		case c == '\r' && !inBacktick:
			// skip carriage returns outside backticks
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 || len(fields) > 0 {
		flushRecord()
	}
	if inBacktick {
		return nil, fmt.Errorf("unterminated backtick in csv")
	}
	return records, nil
}

// CommandDatasetFromCSV builds a Dataset from a CSV file of CommandExamples.
func CommandDatasetFromCSV(path string, seed int64) (*Dataset, error) {
	examples, err := LoadCommandExamplesFromCSV(path)
	if err != nil {
		return nil, err
	}
	if len(examples) == 0 {
		return nil, fmt.Errorf("no command examples found in %s", path)
	}

	samples := make([]Sample, len(examples))
	for i, c := range examples {
		samples[i] = Sample{
			Input: BagOfWords(c.Prompt, CommandVocab),
			Label: LabelForCommand(c.Type),
		}
	}
	return NewDataset(seed, samples...), nil
}

// CommandDatasetFromProto builds a Dataset from a protobuf file of CommandExamples.
func CommandDatasetFromProto(path string, seed int64) (*Dataset, error) {
	examples, err := LoadCommandExamplesFromProto(path)
	if err != nil {
		return nil, err
	}
	if len(examples) == 0 {
		return nil, fmt.Errorf("no command examples found in %s", path)
	}

	samples := make([]Sample, len(examples))
	for i, c := range examples {
		samples[i] = Sample{
			Input: BagOfWords(c.Prompt, CommandVocab),
			Label: LabelForCommand(c.Type),
		}
	}
	return NewDataset(seed, samples...), nil
}

// LoadCommandExamplesFromProto reads CommandExample records from a protobuf file.
func LoadCommandExamplesFromProto(path string) ([]CommandExample, error) {
	pbExamples, err := trainingpb.LoadCommandExamplesFromProto(path)
	if err != nil {
		return nil, err
	}
	examples := make([]CommandExample, len(pbExamples))
	for i, e := range pbExamples {
		examples[i] = CommandExample{
			Type:      e.Type,
			Prompt:    e.UserPrompt,
			Response:  e.AssistantResponse,
			CodeAfter: e.CodeAfter,
		}
	}
	return examples, nil
}

func AppendCommandExample(path string, example CommandExample) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty dataset path")
	}
	if strings.HasSuffix(path, ".pb") {
		examples, err := LoadCommandExamplesFromProto(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("load proto examples: %w", err)
		}
		examples = append(examples, example)
		pbExamples := make([]*trainingpb.CommandExample, 0, len(examples))
		for _, e := range examples {
			pbExamples = append(pbExamples, &trainingpb.CommandExample{
				Type:              e.Type,
				UserPrompt:        e.Prompt,
				AssistantResponse: e.Response,
				CodeAfter:         e.CodeAfter,
			})
		}
		return trainingpb.SaveCommandExamplesToProto(path, pbExamples)
	}

	examples, err := LoadCommandExamplesFromCSV(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load csv examples: %w", err)
	}
	examples = append(examples, example)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer f.Close()
	writer := csv.NewWriter(f)
	if err := writer.Write([]string{"type", "prompt", "response", "code_after"}); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	for _, e := range examples {
		if err := writer.Write([]string{e.Type, e.Prompt, e.Response, e.CodeAfter}); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}
	return nil
}

// NormalizePrompt strips insignificant whitespace and casing so duplicates can be collapsed reliably.
func NormalizePrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	lower := strings.ToLower(trimmed)
	var b strings.Builder
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' || r == '_' || r == '-' {
			if r == ' ' {
				if b.Len() > 0 && b.String()[b.Len()-1] != ' ' {
					b.WriteRune(r)
				}
				continue
			}
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

// DeduplicateCommandExamples drops near-duplicate prompts while preserving the first valid example via a normalized prompt key.
func DeduplicateCommandExamples(examples []CommandExample) []CommandExample {
	seen := make(map[string]bool)
	unique := make([]CommandExample, 0, len(examples))
	for _, ex := range examples {
		if ex.Prompt == "" {
			continue
		}
		normalized := NormalizePrompt(ex.Prompt)
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		unique = append(unique, ex)
	}
	return unique
}

// BalanceClassDistribution keeps a capped sample count per class so dominant classes do not overwhelm the classifier.
func BalanceClassDistribution(examples []CommandExample, maxPerClass int) []CommandExample {
	if maxPerClass <= 0 {
		maxPerClass = 1
	}
	counts := make(map[string]int)
	balanced := make([]CommandExample, 0, len(examples))
	for _, ex := range examples {
		label := strings.TrimSpace(ex.Type)
		if label == "" {
			label = "social"
		}
		if counts[label] >= maxPerClass {
			continue
		}
		counts[label]++
		balanced = append(balanced, ex)
	}
	return balanced
}

// CleanDataset is a small hygiene pass that removes duplicates and balances the class distribution.
func CleanDataset(examples []CommandExample, maxPerClass int) []CommandExample {
	unique := DeduplicateCommandExamples(examples)
	if maxPerClass > 0 {
		return BalanceClassDistribution(unique, maxPerClass)
	}
	return unique
}

// SaveCSV writes a cleaned command dataset back to CSV format.
func SaveCSV(path string, examples []CommandExample) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer f.Close()
	writer := csv.NewWriter(f)
	if err := writer.Write([]string{"type", "prompt", "response", "code_after"}); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	for _, e := range examples {
		if err := writer.Write([]string{e.Type, e.Prompt, e.Response, e.CodeAfter}); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}
	return nil
}

// SaveProto writes cleaned examples back to protobuf.
func SaveProto(path string, examples []CommandExample) error {
	pbExamples := make([]*trainingpb.CommandExample, 0, len(examples))
	for _, ex := range examples {
		pbExamples = append(pbExamples, &trainingpb.CommandExample{
			Type:              ex.Type,
			UserPrompt:        ex.Prompt,
			AssistantResponse: ex.Response,
			CodeAfter:         ex.CodeAfter,
		})
	}
	return trainingpb.SaveCommandExamplesToProto(path, pbExamples)
}

// SaveGob serializes the DenseModel to a gob file.
func (m *DenseModel) SaveGob(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create gob file: %w", err)
	}
	defer f.Close()

	enc := gob.NewEncoder(f)
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("encode model: %w", err)
	}
	return nil
}

// LoadGob deserializes a DenseModel from a gob file.
func LoadGob(path string) (*DenseModel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gob file: %w", err)
	}
	defer f.Close()

	var m DenseModel
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode model: %w", err)
	}
	return &m, nil
}
