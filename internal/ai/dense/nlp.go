package dense

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

var nonAlphaRegex = regexp.MustCompile(`[^a-zA-Z0-9\s]`)

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
