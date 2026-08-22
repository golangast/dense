package dense

import (
	"go/ast"
	"strings"
)

// IntentType represents a resolved natural-language mutation action.
type IntentType string

const (
	IntentReplaceFunc IntentType = "REPLACE_FUNC"
	IntentAddTags     IntentType = "ADD_JSON_TAGS"
	IntentWrapErrors  IntentType = "WRAP_ERRORS"
	IntentUnknown     IntentType = "UNKNOWN"
)

// ResolveIntent maps the prompt and destination AST to a target IntentType.
func ResolveIntent(prompt string, file *ast.File) (IntentType, ScannerParsedPrompt) {
	tokens := ScanPrompt(prompt)

	// Direct token/rule match priority
	if tokens.Action == "replace" && len(tokens.Identifiers) > 0 {
		return IntentReplaceFunc, tokens
	}

	lowerRaw := strings.ToLower(strings.Join(tokens.RawTokens, " "))
	if (strings.Contains(lowerRaw, "json") || strings.Contains(lowerRaw, "tag")) && len(tokens.Identifiers) > 0 {
		return IntentAddTags, tokens
	}

	if (strings.Contains(lowerRaw, "wrap") || strings.Contains(lowerRaw, "return")) && strings.Contains(lowerRaw, "error") && len(tokens.Identifiers) > 0 {
		return IntentWrapErrors, tokens
	}

	return IntentUnknown, tokens
}

// ExecuteMutation dispatches the resolved intent to the correct AST mutators.
func ExecuteMutation(file *ast.File, intent IntentType, parsed ScannerParsedPrompt) bool {
	if file == nil || len(parsed.Identifiers) == 0 {
		return false
	}
	target := parsed.Identifiers[0]
	switch intent {
	case IntentReplaceFunc:
		// Exact replacement logic is handled in the outer loop but we return true to indicate intent.
		return true
	case IntentAddTags:
		return AutoInjectJSONTags(file, target)
	case IntentWrapErrors:
		return WrapErrorsInFunc(file, target)
	}
	return false
}
