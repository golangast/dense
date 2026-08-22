package dense_test

import (
	"testing"

	"github.com/golangast/dense/internal/ai/dense"
)

func TestPredictIntentNLP(t *testing.T) {
	prompt := "please swap function jim for sally() int { return 3 }"
	intent, score := dense.PredictIntentNLP(prompt)

	if intent != dense.IntentReplaceFunc {
		t.Errorf("Expected intent %s, got %s", dense.IntentReplaceFunc, intent)
	}

	if score <= 0.0 {
		t.Errorf("Expected positive confidence score, got %f", score)
	}
}
