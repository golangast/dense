package dense

import "testing"

func TestLoadProjectIndex(t *testing.T) {
	index, err := LoadProjectIndex("../../..")
	if err != nil {
		t.Fatalf("LoadProjectIndex returned error: %v", err)
	}
	if index == nil {
		t.Fatal("index was nil")
	}
	if len(index.Packages) == 0 {
		t.Fatal("expected workspace packages to be indexed")
	}
	if len(index.Symbols) == 0 {
		t.Fatal("expected workspace symbols to be indexed")
	}

	features := GlobalContextFeatures(index, "github.com/golangast/dense/internal/ai/dense")
	if len(features) == 0 {
		t.Fatal("expected global context features")
	}
}
