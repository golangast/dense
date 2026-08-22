package dense

import (
	"strings"
	"unicode"
)

// ParsedSlot describes the resolved intent and target extracted from a prompt.
type ParsedSlot struct {
	Action       string
	TargetSymbol string
	PayloadCode  string
}

var slotVerbSynsets = map[string]string{
	"swap":       "REPLACE",
	"replace":    "REPLACE",
	"substitute": "REPLACE",
	"change":     "REPLACE",
	"tag":        "INJECT_TAGS",
	"annotate":   "INJECT_TAGS",
	"add":        "INJECT_TAGS",
	"inject":     "INJECT_TAGS",
}

// ParsePromptWithSlots extracts intent and target using workspace symbols as anchors.
func ParsePromptWithSlots(prompt string, graph *WorkspaceGraph) ParsedSlot {
	var res ParsedSlot
	if prompt == "" {
		return res
	}

	tokens := strings.Fields(prompt)
	for _, tok := range tokens {
		clean := strings.ToLower(strings.TrimFunc(tok, isSymbolBoundary))
		if clean == "" {
			continue
		}
		if action, ok := slotVerbSynsets[clean]; ok {
			res.Action = action
			break
		}
	}

	if graph != nil {
		for _, tok := range tokens {
			clean := strings.TrimFunc(tok, isSymbolBoundary)
			if clean == "" {
				continue
			}
			if _, exists := graph.Symbols[clean]; exists {
				res.TargetSymbol = clean
				break
			}
			if _, exists := graph.Symbols[strings.Title(clean)]; exists {
				res.TargetSymbol = strings.Title(clean)
				break
			}
		}
		if res.TargetSymbol == "" {
			candidate := findFuzzySymbolMatch(tokens, graph)
			if candidate != "" {
				res.TargetSymbol = candidate
			}
		}
	}

	if res.Action == "" {
		lower := strings.ToLower(prompt)
		if strings.Contains(lower, "json") || strings.Contains(lower, "tag") || strings.Contains(lower, "annotate") {
			res.Action = "INJECT_TAGS"
		}
	}

	if res.PayloadCode == "" {
		payload := extractPayload(prompt)
		if payload != "" {
			res.PayloadCode = payload
		}
	}

	if res.TargetSymbol != "" && res.PayloadCode != "" {
		res.PayloadCode = stripTargetPrefix(res.PayloadCode, res.TargetSymbol)
		res.PayloadCode = stripTargetPrefix(res.PayloadCode, strings.ToLower(res.TargetSymbol))
		res.PayloadCode = stripTargetPrefix(res.PayloadCode, strings.Title(res.TargetSymbol))
		res.PayloadCode = strings.TrimLeft(res.PayloadCode, " \t\n:-")
	}

	return res
}

func stripTargetPrefix(payload, target string) string {
	if target == "" || payload == "" {
		return payload
	}
	if !strings.HasPrefix(payload, target) {
		return payload
	}
	if len(payload) == len(target) {
		return ""
	}
	next := payload[len(target)]
	if unicode.IsLetter(rune(next)) || unicode.IsDigit(rune(next)) || next == '_' {
		return payload
	}
	return strings.TrimSpace(payload[len(target):])
}

func extractPayload(prompt string) string {
	lower := strings.ToLower(prompt)
	for _, marker := range []string{" for ", " with ", " to "} {
		if idx := strings.Index(lower, marker); idx != -1 {
			payload := strings.TrimSpace(prompt[idx+len(marker):])
			payload = strings.TrimSuffix(payload, ".")
			if payload != "" {
				return payload
			}
		}
	}

	if idx := strings.Index(prompt, "("); idx != -1 {
		start := strings.LastIndexAny(prompt[:idx], " \t\n")
		if start == -1 {
			return strings.TrimSpace(prompt)
		}
		payload := strings.TrimSpace(prompt[start+1:])
		if payload != "" {
			return payload
		}
	}

	if idx := strings.Index(prompt, resTargetMarker(prompt)); idx != -1 {
		payload := strings.TrimSpace(prompt[idx+len(resTargetMarker(prompt)):])
		if payload != "" {
			return payload
		}
	}
	return ""
}

func resTargetMarker(prompt string) string {
	for _, tok := range strings.Fields(prompt) {
		clean := strings.TrimFunc(tok, isSymbolBoundary)
		if clean == "" || strings.EqualFold(clean, "for") || strings.EqualFold(clean, "with") || strings.EqualFold(clean, "to") || strings.EqualFold(clean, "swap") || strings.EqualFold(clean, "replace") {
			continue
		}
		if strings.ContainsAny(clean, "(){}[]") {
			continue
		}
		return clean
	}
	return ""
}

func isSymbolBoundary(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
}

func findFuzzySymbolMatch(tokens []string, graph *WorkspaceGraph) string {
	bestMatch := ""
	minDistance := 3

	for _, tok := range tokens {
		clean := strings.TrimFunc(tok, isSymbolBoundary)
		if len(clean) < 3 {
			continue
		}
		for symName := range graph.Symbols {
			dist := slotLevenshteinDistance(strings.ToLower(clean), strings.ToLower(symName))
			if dist < minDistance {
				minDistance = dist
				bestMatch = symName
			}
		}
	}
	return bestMatch
}

func slotLevenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	d := make([][]int, la+1)
	for i := range d {
		d[i] = make([]int, lb+1)
		d[i][0] = i
	}
	for j := 0; j <= lb; j++ {
		d[0][j] = j
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = slotMinInt(d[i-1][j]+1, slotMinInt(d[i][j-1]+1, d[i-1][j-1]+cost))
		}
	}
	return d[la][lb]
}

func slotMinInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
