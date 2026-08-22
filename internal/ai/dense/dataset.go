package dense

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Sample is a plain, strongly-typed training example. No JSON, no CSV, no
// pipeline — just an explicit Go struct pair (Input -> Label).
//
// Input is a precomputed feature vector (e.g. bag-of-words counts or a tiny
// ASCII/character encoding). Label is the target class index.
type Sample struct {
	Input []float32
	Label int
}

// Dataset is an ordered slice of samples. Training walks it sequentially with
// a fixed, deterministic permutation so results are reproducible.
type Dataset struct {
	Samples []Sample
	Seed    int64
}

// NewDataset builds a dataset from already-encoded samples.
func NewDataset(seed int64, samples ...Sample) *Dataset {
	return &Dataset{Samples: samples, Seed: seed}
}

// FeatureSize returns the input vector dimension (or 0 when empty).
func (d *Dataset) FeatureSize() int {
	if len(d.Samples) == 0 {
		return 0
	}
	return len(d.Samples[0].Input)
}

// NumClasses returns the number of distinct labels.
func (d *Dataset) NumClasses() int {
	maxLabel := -1
	for _, s := range d.Samples {
		if s.Label > maxLabel {
			maxLabel = s.Label
		}
	}
	return maxLabel + 1
}

// Bounds guards against malformed samples.
func (d *Dataset) Bounds() error {
	if len(d.Samples) == 0 {
		return &errDim{index: -1, msg: "empty dataset"}
	}
	n := len(d.Samples[0].Input)
	for i, s := range d.Samples {
		if len(s.Input) != n {
			return &errDim{index: i, got: len(s.Input), want: n}
		}
		if s.Label < 0 {
			return &errDim{index: i, msg: "label must be >= 0"}
		}
	}
	return nil
}

type errDim struct {
	index int
	got   int
	want  int
	msg   string
}

func (e *errDim) Error() string {
	if e.msg != "" {
		return "dataset: " + e.msg + " at sample " + itoa(e.index)
	}
	return "dataset: inconsistent feature size at sample " + itoa(e.index) +
		" (got " + itoa(e.got) + ", want " + itoa(e.want) + ")"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ─── Encoders ────────────────────────────────────────────────────────────────

// CharEncode converts a fixed-alphabet token into a one-hot feature vector.
// Alphabet is ordered; unknown characters map to the final "UNK" slot.
func CharEncode(token string, alphabet string) []float32 {
	vec := make([]float32, len(alphabet)+1)
	for _, r := range token {
		idx := -1
		for i, c := range alphabet {
			if c == r {
				idx = i
				break
			}
		}
		if idx < 0 {
			idx = len(alphabet) // UNK
		}
		vec[idx] = 1
	}
	return vec
}

// ASTContextFeatures summarizes the structural state of a Go source file in a
// small dense binary vector intended to be concatenated with the prompt bag.
type ASTContextFeatures struct {
	HasPackageDecl   bool
	InsideFunc       bool
	InsideStruct     bool
	InsideInterface  bool
	HasImportFmt     bool
	HasImportContext bool
	HasImportTesting bool
	FuncCount        int
	TypeCount        int
	ReturnCheckCount int
	PackageFiles     []string
}

// CodeASTFeatures is the package-aware version of the AST context summary.
type CodeASTFeatures = ASTContextFeatures

func (a ASTContextFeatures) Vector() []float32 {
	return []float32{
		boolFloat(a.HasPackageDecl),
		boolFloat(a.InsideFunc),
		boolFloat(a.InsideStruct),
		boolFloat(a.InsideInterface),
		boolFloat(a.HasImportFmt),
		boolFloat(a.HasImportContext),
		boolFloat(a.HasImportTesting),
		float32(a.FuncCount),
		float32(a.TypeCount),
		float32(a.ReturnCheckCount),
	}
}

func boolFloat(v bool) float32 {
	if v {
		return 1
	}
	return 0
}

func ExtractASTContextFeatures(src string) ASTContextFeatures {
	features := ASTContextFeatures{}
	if strings.TrimSpace(src) == "" {
		return features
	}
	if strings.Contains(src, "package ") {
		features.HasPackageDecl = true
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return features
	}
	features.HasPackageDecl = file.Name != nil && file.Name.Name != ""
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value == "" {
			continue
		}
		pkg := strings.Trim(imp.Path.Value, `"`)
		switch pkg {
		case "fmt":
			features.HasImportFmt = true
		case "context":
			features.HasImportContext = true
		case "testing":
			features.HasImportTesting = true
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			features.InsideFunc = true
			features.FuncCount++
		case *ast.FuncLit:
			features.InsideFunc = true
		case *ast.TypeSpec:
			switch x.Type.(type) {
			case *ast.StructType:
				features.InsideStruct = true
			case *ast.InterfaceType:
				features.InsideInterface = true
			}
			features.TypeCount++
		case *ast.IfStmt:
			if isErrCheck(x.Cond) {
				features.ReturnCheckCount++
			}
		}
		return true
	})
	return features
}

func ASTContextFeatureVector(src string) []float32 {
	return ExtractASTContextFeatures(src).Vector()
}

func ExtractPackageASTContextFeatures(path string) ASTContextFeatures {
	features := ASTContextFeatures{}
	if strings.TrimSpace(path) == "" {
		return features
	}

	resolved := path
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		resolved = path
	} else if err == nil && !info.IsDir() {
		resolved = filepath.Dir(path)
	}
	if err != nil {
		return features
	}

	files, err := os.ReadDir(resolved)
	if err != nil {
		return features
	}

	for _, entry := range files {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".go") {
			continue
		}
		p := filepath.Join(resolved, entry.Name())
		features.PackageFiles = append(features.PackageFiles, p)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f := ExtractASTContextFeatures(string(b))
		features.HasPackageDecl = features.HasPackageDecl || f.HasPackageDecl
		features.InsideFunc = features.InsideFunc || f.InsideFunc
		features.InsideStruct = features.InsideStruct || f.InsideStruct
		features.InsideInterface = features.InsideInterface || f.InsideInterface
		features.HasImportFmt = features.HasImportFmt || f.HasImportFmt
		features.HasImportContext = features.HasImportContext || f.HasImportContext
		features.HasImportTesting = features.HasImportTesting || f.HasImportTesting
		features.FuncCount += f.FuncCount
		features.TypeCount += f.TypeCount
		features.ReturnCheckCount += f.ReturnCheckCount
	}
	return features
}

func ContextualFeatureVector(prompt, src string, vocab []string) []float32 {
	base := BagOfWords(prompt, vocab)
	if src == "" {
		return base
	}
	astVec := ASTContextFeatureVector(src)
	out := make([]float32, 0, len(base)+len(astVec))
	out = append(out, base...)
	out = append(out, astVec...)
	return out
}

func countErrReturnChecks(body *ast.BlockStmt) int {
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		if stmt, ok := n.(*ast.IfStmt); ok && isErrCheck(stmt.Cond) {
			count++
		}
		return true
	})
	return count
}

func isErrCheck(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		if e.Op != token.NEQ && e.Op != token.EQL {
			return false
		}
		return isErrLike(e.X) || isErrLike(e.Y)
	case *ast.Ident:
		return isErrLike(e)
	case *ast.SelectorExpr:
		return isErrLike(e)
	default:
		return false
	}
}

func isErrLike(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return strings.Contains(strings.ToLower(e.Name), "err")
	case *ast.SelectorExpr:
		return isErrLike(e.Sel)
	case *ast.CallExpr:
		return false
	case *ast.ParenExpr:
		return isErrLike(e.X)
	default:
		return false
	}
}

func hasErrResult(ret *ast.ReturnStmt) bool {
	for _, r := range ret.Results {
		if isErrLike(r) {
			return true
		}
	}
	return false
}

func TokenizeForBagOfWords(prompt string) []string {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	parts := strings.FieldsFunc(prompt, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(parts)*2)
	for _, p := range parts {
		for _, token := range splitIdentifierTerms(p) {
			if token == "" {
				continue
			}
			low := strings.ToLower(token)
			if !seen[low] {
				seen[low] = true
				out = append(out, low)
			}
			for _, gram := range charNgrams(low, 3) {
				if !seen[gram] {
					seen[gram] = true
					out = append(out, gram)
				}
			}
		}
	}
	return out
}

func splitIdentifierTerms(raw string) []string {
	if raw == "" {
		return nil
	}
	runes := []rune(raw)
	terms := make([]string, 0, len(runes))
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		term := strings.TrimSpace(b.String())
		if term != "" {
			terms = append(terms, term)
		}
		b.Reset()
	}
	for i, r := range runes {
		if r == '_' || r == '-' || r == '.' {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if (unicode.IsLower(prev) || unicode.IsDigit(prev)) || (unicode.IsUpper(prev) && next != 0 && unicode.IsLower(next)) {
				flush()
			}
		}
		b.WriteRune(r)
	}
	flush()
	return terms
}

func charNgrams(token string, n int) []string {
	if n <= 0 || len(token) < n {
		return nil
	}
	out := make([]string, 0, len(token)-n+1)
	for i := 0; i+n <= len(token); i++ {
		out = append(out, token[i:i+n])
	}
	return out
}

// BagOfWords encodes a short natural-language prompt as a binary presence
// vector over a fixed vocabulary. Any identifier fragments and 3-character
// subwords are lowered and matched against the vocabulary, which allows a
// prompt like "ValidateUser" or "compute_sum" to map to usable dense features.
func BagOfWords(prompt string, vocab []string) []float32 {
	vec := make([]float32, len(vocab))
	if len(vocab) == 0 {
		return vec
	}
	for _, w := range TokenizeForBagOfWords(prompt) {
		for i, v := range vocab {
			if w == strings.ToLower(v) {
				vec[i] = 1
				break
			}
		}
	}
	return vec
}
