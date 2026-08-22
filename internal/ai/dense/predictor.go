package dense

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var camelMatcher = regexp.MustCompile("([a-z0-9])([A-Z])")

// CommandPredictor remains as a compatibility alias for the production Predictor API.
type CommandPredictor = Predictor

// Predictor tracks historical command sequences to guess the developer's next step.
type Predictor struct {
	mu          sync.RWMutex
	Transitions map[string]map[string]int `json:"transitions"`
}

// NewPredictor creates a thread-safe state-transition matrix for sequential commands.
func NewPredictor() *Predictor {
	return &Predictor{Transitions: make(map[string]map[string]int)}
}

// RecordSequence records that action B occurred immediately after action A.
func (p *Predictor) RecordSequence(lastAction, nextAction string) {
	if p == nil || strings.TrimSpace(lastAction) == "" || strings.TrimSpace(nextAction) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Transitions[lastAction] == nil {
		p.Transitions[lastAction] = make(map[string]int)
	}
	p.Transitions[lastAction][nextAction]++
}

// PredictNext returns the most likely next actions for a given history state, ordered by frequency.
func (p *Predictor) PredictNext(lastAction string, limit int) []string {
	if limit <= 0 {
		limit = 1
	}
	p.mu.RLock()
	nextMap, exists := p.Transitions[lastAction]
	p.mu.RUnlock()
	if !exists || len(nextMap) == 0 {
		return p.fallbackSuggestions(lastAction)
	}

	type candidate struct {
		action string
		count  int
	}
	var candidates []candidate
	for act, cnt := range nextMap {
		candidates = append(candidates, candidate{action: act, count: cnt})
	}
	if len(candidates) > 1 {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].count > candidates[j].count })
	}
	results := make([]string, 0, min(limit, len(candidates)))
	for i := 0; i < len(candidates) && i < limit; i++ {
		results = append(results, candidates[i].action)
	}
	if len(results) == 0 {
		return p.fallbackSuggestions(lastAction)
	}
	return results
}

// TokenizeCodePrompt splits mixed identifiers and command strings into lower-case subwords.
func TokenizeCodePrompt(input string) []string {
	if input == "" {
		return nil
	}
	s := camelMatcher.ReplaceAllString(input, "${1} ${2}")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "/", " ")
	return strings.Fields(strings.ToLower(s))
}

// LevenshteinDistance computes edit distance between two strings.
func LevenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			if del < ins {
				ins = del
			}
			if sub < ins {
				ins = sub
			}
			curr[j] = ins
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func (p *Predictor) fallbackSuggestions(lastAction string) []string {
	switch strings.ToUpper(strings.TrimSpace(lastAction)) {
	case "ADD_FUNC", "ADD_FUNCTION":
		return []string{"add unit test", "add error check", "add doc comment"}
	case "ADD_STRUCT":
		return []string{"add json tags", "add constructor New...", "implement String()"}
	case "ADD_IMPORT":
		return []string{"check unused imports"}
	default:
		return []string{"add function", "add unit test"}
	}
}

// SaveToFile persists transition history to a local JSON file.
func (p *Predictor) SaveToFile(path string) error {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	data, err := json.MarshalIndent(p.Transitions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// LoadFromFile restores transition history from disk.
func (p *Predictor) LoadFromFile(path string) error {
	if p == nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(data) == 0 {
		p.Transitions = make(map[string]map[string]int)
		return nil
	}
	return json.Unmarshal(data, &p.Transitions)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// CommandPredictorRecordSequence is a compatibility helper for older call sites.
func (p *Predictor) CommandPredictorRecordSequence(cmdA, cmdB string) {
	p.RecordSequence(cmdA, cmdB)
}

// CommandPredictorPredictNext is a compatibility helper for older call sites.
func (p *Predictor) CommandPredictorPredictNext(currentCmd string) string {
	cands := p.PredictNext(currentCmd, 1)
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}

// Backward compatibility for earlier code styles.
func (p *Predictor) RecordSequenceLegacy(cmdA, cmdB string) {
	p.RecordSequence(cmdA, cmdB)
}

func (p *Predictor) PredictNextLegacy(currentCmd string) string {
	return p.CommandPredictorPredictNext(currentCmd)
}
