package dense

import _ "embed"

//go:embed weights.txt
var EmbeddedWeights []byte

// LoadEmbeddedModel initializes a neural network using compiled-in weights.
func LoadEmbeddedModel() (*NeuralNet, error) {
	return DecodeWeights(EmbeddedWeights)
}
