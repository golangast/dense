package dense

import (
	"go/ast"
	"math"
	"testing"
)

func TestExtractCombinedFeatures_IncludesPromptAndASTSignal(t *testing.T) {
	fn := &ast.FuncDecl{
		Type: &ast.FuncType{
			TypeParams: &ast.FieldList{},
			Params:     &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{{Name: "name"}}, Type: ast.NewIdent("string")}}},
			Results:    &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}},
		},
		Recv: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{{Name: "r"}}, Type: &ast.StarExpr{X: ast.NewIdent("Receiver")}}}},
	}

	vec := ExtractCombinedFeatures("replace Jim with func Jim() string { return \"jim\" }", fn)
	if len(vec) < len(CommandVocab)+5 {
		t.Fatalf("combined feature length = %d, want at least %d", len(vec), len(CommandVocab)+5)
	}
	if vec[len(CommandVocab)] != 1.0 {
		t.Fatalf("generics bit should be 1.0, got %v", vec[len(CommandVocab)])
	}
	if vec[len(CommandVocab)+1] != 1.0 {
		t.Fatalf("return count bit should be 1.0, got %v", vec[len(CommandVocab)+1])
	}
	if vec[len(CommandVocab)+2] != 1.0 {
		t.Fatalf("receiver bit should be 1.0, got %v", vec[len(CommandVocab)+2])
	}
	if vec[len(CommandVocab)+3] != 1.0 {
		t.Fatalf("param count bit should be 1.0, got %v", vec[len(CommandVocab)+3])
	}
}

func TestNeuralNetForward_ProducesValidSoftmaxOutput(t *testing.T) {
	nn := &NeuralNet{
		Weights1: [][]float64{{0.5, -0.2}, {0.3, 0.1}},
		Weights2: [][]float64{{0.8}, {0.6}},
		Biases1:  []float64{0.1, -0.1},
		Biases2:  []float64{0.2},
	}

	out := nn.Forward([]float64{1.0, 0.5})
	if len(out) != 1 {
		t.Fatalf("output len = %d, want 1", len(out))
	}
	if math.IsNaN(out[0]) || math.IsInf(out[0], 0) {
		t.Fatalf("output is not finite: %v", out)
	}
	if math.Abs(out[0]-1.0) > 1e-9 {
		t.Fatalf("softmax probability = %v, want 1.0", out[0])
	}
}

func BenchmarkForwardPass(b *testing.B) {
	nn := NewNeuralNet()
	features := make([]float64, 128)
	for i := range features {
		features[i] = float64(i%7) * 0.1
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = nn.Forward(features)
	}
}
