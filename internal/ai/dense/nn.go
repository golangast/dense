package dense

import (
	"fmt"
	"go/ast"
	"math"
)

// NeuralNet is a minimal pure-Go feedforward network using ReLU in the hidden
// layer and softmax in the output layer.
type NeuralNet struct {
	Weights1 [][]float64
	Weights2 [][]float64
	Biases1  []float64
	Biases2  []float64
}

func NewNeuralNet() *NeuralNet {
	hidden := 8
	outDim := 4
	inDim := 128
	weights1 := make([][]float64, inDim)
	for i := range weights1 {
		weights1[i] = make([]float64, hidden)
		for j := range weights1[i] {
			weights1[i][j] = 0.01 * float64((i+j)%7-3)
		}
	}
	weights2 := make([][]float64, hidden)
	for i := range weights2 {
		weights2[i] = make([]float64, outDim)
		for j := range weights2[i] {
			weights2[i][j] = 0.01 * float64((i+j)%5-2)
		}
	}
	biases1 := make([]float64, hidden)
	biases2 := make([]float64, outDim)
	return &NeuralNet{Weights1: weights1, Weights2: weights2, Biases1: biases1, Biases2: biases2}
}

func ReLU(x float64) float64 {
	if x > 0 {
		return x
	}
	return 0
}

func Softmax(input []float64) []float64 {
	if len(input) == 0 {
		return nil
	}
	out := make([]float64, len(input))
	max := input[0]
	for _, v := range input[1:] {
		if v > max {
			max = v
		}
	}
	var sum float64
	for i, v := range input {
		out[i] = math.Exp(v - max)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

func (nn *NeuralNet) Predict(features []float64) int {
	if nn == nil || len(features) == 0 {
		return 0
	}
	output := nn.Forward(features)
	if len(output) == 0 {
		return 0
	}
	best := 0
	for i := 1; i < len(output); i++ {
		if output[i] > output[best] {
			best = i
		}
	}
	return best
}

func (nn *NeuralNet) Forward(features []float64) []float64 {
	if nn == nil {
		return nil
	}
	if len(nn.Biases1) == 0 || len(nn.Biases2) == 0 {
		return nil
	}
	if len(nn.Weights1) == 0 || len(nn.Weights2) == 0 {
		return nil
	}
	if len(features) != len(nn.Weights1) {
		return nil
	}

	hidden := make([]float64, len(nn.Biases1))
	for i := range hidden {
		var sum float64
		for j, feat := range features {
			if j >= len(nn.Weights1) {
				break
			}
			sum += feat * nn.Weights1[j][i]
		}
		hidden[i] = ReLU(sum + nn.Biases1[i])
	}

	output := make([]float64, len(nn.Biases2))
	for i := range output {
		var sum float64
		for j, h := range hidden {
			if j >= len(nn.Weights2) {
				break
			}
			sum += h * nn.Weights2[j][i]
		}
		output[i] = sum + nn.Biases2[i]
	}

	return Softmax(output)
}

// ExtractCombinedFeatures concatenates prompt bag-of-words features with a small
// structural AST feature vector. The prompt vector is the library's standard
// binary presence encoding; the AST tail adds generic/return/receiver/param info.
func ExtractCombinedFeatures(prompt string, fn *ast.FuncDecl) []float64 {
	textVec := make([]float64, len(CommandVocab))
	for _, token := range TokenizeForBagOfWords(prompt) {
		for i, vocabToken := range CommandVocab {
			if token == vocabToken {
				textVec[i] = 1.0
				break
			}
		}
	}

	astVec := []float64{0, 0, 0, 0, 0}
	if fn != nil {
		if fn.Type != nil && fn.Type.TypeParams != nil {
			astVec[0] = 1.0
		}
		if fn.Type != nil && fn.Type.Results != nil {
			astVec[1] = float64(len(fn.Type.Results.List))
		}
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			astVec[2] = 1.0
		}
		if fn.Type != nil && fn.Type.Params != nil {
			astVec[3] = float64(len(fn.Type.Params.List))
		}
		if fn.Body != nil {
			astVec[4] = 1.0
		}
	}

	out := make([]float64, 0, len(textVec)+len(astVec))
	out = append(out, textVec...)
	out = append(out, astVec...)
	return out
}

func ExampleNeuralNet() {
	_ = fmt.Sprintf("%v", NeuralNet{})
}
