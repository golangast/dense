package dense

import (
	"fmt"
	"strconv"
	"strings"
)

func DecodeWeights(data []byte) (*NeuralNet, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty weights payload")
	}
	if len(fields) < 3 {
		return nil, fmt.Errorf("insufficient weights payload")
	}

	inDim := 128
	hidden := 8
	outDim := 4
	weights1 := make([][]float64, inDim)
	for i := 0; i < inDim; i++ {
		weights1[i] = make([]float64, hidden)
		for j := 0; j < hidden; j++ {
			idx := i*hidden + j
			if idx >= len(fields) {
				return nil, fmt.Errorf("weights file truncated: expected %d entries, got %d", inDim*hidden+hidden*outDim+hidden+outDim, len(fields))
			}
			v, err := strconv.ParseFloat(fields[idx], 64)
			if err != nil {
				return nil, fmt.Errorf("parse weight1[%d][%d]: %w", i, j, err)
			}
			weights1[i][j] = v
		}
	}
	pos := inDim * hidden
	weights2 := make([][]float64, hidden)
	for i := 0; i < hidden; i++ {
		weights2[i] = make([]float64, outDim)
		for j := 0; j < outDim; j++ {
			idx := pos + i*outDim + j
			if idx >= len(fields) {
				return nil, fmt.Errorf("weights file truncated: expected %d entries, got %d", inDim*hidden+hidden*outDim+hidden+outDim, len(fields))
			}
			v, err := strconv.ParseFloat(fields[idx], 64)
			if err != nil {
				return nil, fmt.Errorf("parse weight2[%d][%d]: %w", i, j, err)
			}
			weights2[i][j] = v
		}
	}
	pos = inDim*hidden + hidden*outDim
	biases1 := make([]float64, hidden)
	for i := 0; i < hidden; i++ {
		idx := pos + i
		if idx >= len(fields) {
			return nil, fmt.Errorf("weights file truncated: expected %d entries, got %d", inDim*hidden+hidden*outDim+hidden+outDim, len(fields))
		}
		v, err := strconv.ParseFloat(fields[idx], 64)
		if err != nil {
			return nil, fmt.Errorf("parse bias1[%d]: %w", i, err)
		}
		biases1[i] = v
	}
	pos += hidden
	biases2 := make([]float64, outDim)
	for i := 0; i < outDim; i++ {
		idx := pos + i
		if idx >= len(fields) {
			return nil, fmt.Errorf("weights file truncated: expected %d entries, got %d", inDim*hidden+hidden*outDim+hidden+outDim, len(fields))
		}
		v, err := strconv.ParseFloat(fields[idx], 64)
		if err != nil {
			return nil, fmt.Errorf("parse bias2[%d]: %w", i, err)
		}
		biases2[i] = v
	}
	return &NeuralNet{Weights1: weights1, Weights2: weights2, Biases1: biases1, Biases2: biases2}, nil
}
