package dense

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

var nonAlphaRegex = regexp.MustCompile(`[^a-zA-Z0-9\s]`)

var VerbSynsets = map[string]string{
	"swap":       "REPLACE",
	"replace":    "REPLACE",
	"substitute": "REPLACE",
	"change":     "REPLACE",
	"update":     "REPLACE",
	"inject":     "ADD_TAGS",
	"annotate":   "ADD_TAGS",
	"tag":        "ADD_TAGS",
	"label":      "ADD_TAGS",
	"attach":     "ADD_TAGS",
	"add":        "ADD_TAGS",
	"wrap":       "WRAP",
	"decorate":   "WRAP",
}

// PromptSlots holds a lightweight, semantic breakdown of a natural-language prompt.
type PromptSlots struct {
	Action  string
	Target  string
	Type    string
	Payload string
	Tokens  []string
}

// IntentClass is kept as an alias to the project’s existing intent enum so the
// NLP pipeline remains compatible with the rest of the AST mutation engine.
type IntentClass = IntentType

// IntentCorpus is a lightweight corpus of natural-language prompts for the
// intent classifier. It is intentionally small and high-signal.
var IntentCorpus = map[IntentClass][]string{
	IntentReplaceFunc: {
		"replace function with new implementation",
		"swap method declaration for updated code",
		"change fn body to return value",
		"substitute target with source",
	},
	IntentAddTags: {
		"add json tags to struct",
		"inject field tags into struct model",
		"populate struct with json labels",
	},
}

// TokenizePrompt breaks down complex code prompts into normalized subwords and bi-grams.
func TokenizePrompt(prompt string) []string {
	cleaned := nonAlphaRegex.ReplaceAllString(prompt, " ")
	words := strings.Fields(cleaned)

	var tokens []string
	for _, word := range words {
		for _, subword := range splitIdentifier(word) {
			if subword == "" {
				continue
			}
			tokens = append(tokens, strings.ToLower(subword))
		}
	}

	var biGrams []string
	for i := 0; i < len(tokens)-1; i++ {
		biGrams = append(biGrams, tokens[i]+"_"+tokens[i+1])
	}

	return append(tokens, biGrams...)
}

func normalizeSymbolCandidate(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(strings.TrimSuffix(s, "."), " ")
	s = regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(s, "")
	return strings.ToLower(s)
}

func levenshteinDistance(a, b string) int {
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
		copy(prev, curr)
	}
	return prev[len(b)]
}

func ParsePromptSlots(prompt string) PromptSlots {
	out := PromptSlots{Action: "UNKNOWN"}
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return out
	}

	lower := strings.ToLower(trimmed)
	for word, action := range VerbSynsets {
		if strings.Contains(lower, word) {
			out.Action = action
			break
		}
	}

	var targetPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:swap|replace|change|update|substitute)\s+(?:function|fn|method|func|type|struct)?\s*([A-Za-z_][A-Za-z0-9_]*)\s+(?:for|with|to)\s+(.+)$`),
		regexp.MustCompile(`(?i)(?:add|inject|annotate|tag|label|attach)\s+(?:json\s+)?(?:tags?|labels?)\s+(?:to|into|in)\s+([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`(?i)(?:add|inject|annotate|tag|label|attach)\s+(?:json\s+)?(?:tags?|labels?)\s+(?:to|into|in)\s+([A-Za-z_][A-Za-z0-9_]*)\s+([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`(?i)(?:for|with|to)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\{|\()`),
	}
	for _, re := range targetPatterns {
		if m := re.FindStringSubmatch(trimmed); len(m) > 1 {
			if out.Action == "REPLACE" || out.Action == "ADD_TAGS" {
				out.Target = m[1]
				if len(m) > 2 && strings.TrimSpace(m[2]) != "" {
					out.Payload = strings.TrimSpace(m[2])
				}
				break
			}
		}
	}
	if out.Target == "" {
		for _, part := range strings.Fields(trimmed) {
			if part == "" || strings.ContainsAny(part, "(){}[];,:/\\") {
				continue
			}
			candidate := strings.Trim(part, "\"'`.")
			if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(candidate) {
				if !isStopWord(candidate) {
					out.Target = candidate
					break
				}
			}
		}
	}
	if out.Payload == "" {
		for _, keyword := range []string{"for", "with", "to"} {
			idx := strings.Index(strings.ToLower(trimmed), keyword)
			if idx >= 0 {
				candidate := strings.TrimSpace(trimmed[idx+len(keyword):])
				candidate = strings.TrimSuffix(candidate, ".")
				if strings.Contains(candidate, " ") {
					out.Payload = candidate
					break
				}
			}
		}
	}
	if out.Action == "UNKNOWN" {
		if strings.Contains(lower, "tag") || strings.Contains(lower, "json") {
			out.Action = "ADD_TAGS"
		}
	}
	out.Tokens = TokenizePrompt(prompt)
	return out
}

func isStopWord(word string) bool {
	stopWords := map[string]bool{
		"please": true, "swap": true, "replace": true, "function": true, "fn": true,
		"method": true, "for": true, "with": true, "to": true, "in": true, "add": true,
		"inject": true, "annotate": true, "tag": true, "json": true, "struct": true,
		"model": true, "type": true, "the": true, "a": true, "an": true,
	}
	return stopWords[strings.ToLower(word)]
}

func ResolvePromptTarget(graph *WorkspaceGraph, prompt string) (string, bool) {
	if graph == nil {
		return "", false
	}
	candidate := ParsePromptSlots(prompt).Target
	if candidate == "" {
		for _, token := range TokenizePrompt(prompt) {
			if token == "" || isStopWord(token) {
				continue
			}
			candidate = token
			break
		}
	}
	if candidate == "" {
		return "", false
	}

	if sym, ok := graph.FindSymbol(candidate); ok {
		return sym.Name, true
	}
	if sym, ok := graph.FindSymbol(strings.Title(candidate)); ok {
		return sym.Name, true
	}

	bestName := ""
	bestDistance := 1000000
	for _, sym := range graph.Symbols {
		normSym := normalizeSymbolCandidate(sym.Name)
		normTarget := normalizeSymbolCandidate(candidate)
		if normSym == "" || normTarget == "" {
			continue
		}
		dist := levenshteinDistance(normTarget, normSym)
		if dist < bestDistance {
			bestDistance = dist
			bestName = sym.Name
		}
	}
	if bestDistance <= 3 || strings.Contains(strings.ToLower(bestName), strings.ToLower(candidate)) {
		return bestName, true
	}
	return "", false
}

func splitIdentifier(s string) []string {
	if s == "" {
		return nil
	}

	var words []string
	var current strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if r == '_' || r == '-' {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
			continue
		}

		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && next != 0 && unicode.IsLower(next)) {
				if current.Len() > 0 {
					words = append(words, current.String())
					current.Reset()
				}
			}
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// PredictIntentNLP computes a cosine-similarity score across the intent corpus.
func PredictIntentNLP(prompt string) (IntentClass, float64) {
	tokens := TokenizePrompt(prompt)
	tf := make(map[string]float64)
	for _, token := range tokens {
		tf[token]++
	}

	bestClass := IntentReplaceFunc
	bestScore := 0.0
	for intent, corpus := range IntentCorpus {
		score := 0.0
		for _, doc := range corpus {
			sim := cosineSimilarity(tf, TokenizePrompt(doc))
			if sim > score {
				score = sim
			}
		}
		if score > bestScore {
			bestScore = score
			bestClass = intent
		}
	}

	return bestClass, bestScore
}

func cosineSimilarity(tf map[string]float64, docTokens []string) float64 {
	docTF := make(map[string]float64)
	for _, token := range docTokens {
		docTF[token]++
	}

	dotProduct := 0.0
	for token, count := range tf {
		if docCount, exists := docTF[token]; exists {
			dotProduct += count * docCount
		}
	}

	magA := 0.0
	for _, count := range tf {
		magA += count * count
	}
	magB := 0.0
	for _, count := range docTF {
		magB += count * count
	}
	if magA == 0 || magB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(magA) * math.Sqrt(magB))
}
