package knowledge

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Resource models a structured learning resource extracted from a document.
type Resource struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Category   string `json:"category"`
	Author     string `json:"author"`
	Phase      int    `json:"phase"`
	Difficulty string `json:"difficulty"`
	Notes      string `json:"notes"`
}

// ResourceSet is a catalog of resources with filtering helpers.
type ResourceSet struct {
	Resources []Resource
}

func (r ResourceSet) FilterByAuthor(author string) []Resource {
	needle := strings.ToLower(strings.TrimSpace(author))
	out := make([]Resource, 0)
	for _, res := range r.Resources {
		if strings.Contains(strings.ToLower(res.Author), needle) {
			out = append(out, res)
		}
	}
	return out
}

func (r ResourceSet) FilterByCategory(category string) []Resource {
	needle := strings.ToLower(strings.TrimSpace(category))
	out := make([]Resource, 0)
	for _, res := range r.Resources {
		if strings.Contains(strings.ToLower(res.Category), needle) {
			out = append(out, res)
		}
	}
	return out
}

func (r ResourceSet) ByPhase(phase int) []Resource {
	out := make([]Resource, 0)
	for _, res := range r.Resources {
		if res.Phase == phase {
			out = append(out, res)
		}
	}
	return out
}

func (r ResourceSet) SortedByPhase() []Resource {
	out := append([]Resource(nil), r.Resources...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Phase == out[j].Phase {
			return out[i].Title < out[j].Title
		}
		return out[i].Phase < out[j].Phase
	})
	return out
}

func (r ResourceSet) ExportJSON() ([]byte, error) {
	return json.MarshalIndent(r.Resources, "", "  ")
}

func (r ResourceSet) ExportMarkdown() string {
	var b strings.Builder
	for _, res := range r.SortedByPhase() {
		b.WriteString(fmt.Sprintf("## Phase %d — %s\n", res.Phase, res.Title))
		b.WriteString(fmt.Sprintf("- Category: %s\n", res.Category))
		b.WriteString(fmt.Sprintf("- Author: %s\n", res.Author))
		b.WriteString(fmt.Sprintf("- URL: %s\n", res.URL))
		if res.Notes != "" {
			b.WriteString(fmt.Sprintf("- Notes: %s\n", res.Notes))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ExtractResourcesFromDOCX reads a DOCX file and extracts candidate resource rows.
func ExtractResourcesFromDOCX(path string) ([]Resource, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var docXML string
	for _, f := range reader.File {
		if f.Name == "word/document.xml" || strings.HasSuffix(f.Name, "/document.xml") {
			in, err := f.Open()
			if err != nil {
				return nil, err
			}
			content, err := io.ReadAll(in)
			in.Close()
			if err != nil {
				return nil, err
			}
			docXML = string(content)
			break
		}
	}
	if docXML == "" {
		return nil, fmt.Errorf("document.xml not found in %s", path)
	}
	return parseDocxText(docXML)
}

func parseDocxText(docXML string) ([]Resource, error) {
	resources := make([]Resource, 0)
	currentCategory := "General"
	textMatches := regexp.MustCompile(`(?s)<w:t[^>]*>(.*?)</w:t>`).FindAllStringSubmatch(docXML, -1)
	if len(textMatches) == 0 {
		return resources, nil
	}
	for _, match := range textMatches {
		if len(match) < 2 {
			continue
		}
		line := strings.TrimSpace(match[1])
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, "&amp;", "&")
		line = strings.ReplaceAll(line, "&lt;", "<")
		line = strings.ReplaceAll(line, "&gt;", ">")
		if isCategoryHeader(line) {
			currentCategory = line
			continue
		}
		if url := extractURL(line); url != "" {
			author := extractAuthor(line)
			resources = append(resources, Resource{
				Title:      strings.TrimSpace(author),
				URL:        normalizeURL(url),
				Category:   currentCategory,
				Author:     author,
				Phase:      inferPhase(line),
				Difficulty: inferDifficulty(line),
				Notes:      "Extracted from DOCX",
			})
		}
	}
	return resources, nil
}

func isCategoryHeader(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") {
		return false
	}
	return strings.Contains(lower, "concurrency") || strings.Contains(lower, "interfaces") || strings.Contains(lower, "ast") || strings.Contains(lower, "testing") || strings.Contains(lower, "wasm") || strings.Contains(lower, "data structures") || strings.Contains(lower, "performance")
}

func extractURL(s string) string {
	m := regexp.MustCompile(`https?://[^\s\)\]<>]+`).FindString(s)
	if m == "" {
		m = regexp.MustCompile(`www\.[^\s\)\]<>]+`).FindString(s)
		if m != "" {
			return "https://" + m
		}
	}
	return m
}

func extractAuthor(s string) string {
	if url := extractURL(s); url != "" {
		prefix := strings.TrimSpace(strings.Replace(s, url, "", 1))
		prefix = strings.Trim(prefix, "-: ")
		if prefix != "" {
			return prefix
		}
	}
	return "Unknown"
}

func normalizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimRight(trimmed, ".,;)")
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	return "https://" + trimmed
}

func inferPhase(s string) int {
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "phase 1") || strings.Contains(lower, "tour") || strings.Contains(lower, "effective go") || strings.Contains(lower, "learn.go") || strings.Contains(lower, "go by example"):
		return 1
	case strings.Contains(lower, "phase 2") || strings.Contains(lower, "http") || strings.Contains(lower, "database") || strings.Contains(lower, "slice") || strings.Contains(lower, "sync.pool"):
		return 2
	case strings.Contains(lower, "phase 3") || strings.Contains(lower, "interface") || strings.Contains(lower, "goroutine") || strings.Contains(lower, "generics") || strings.Contains(lower, "ast"):
		return 3
	case strings.Contains(lower, "phase 4") || strings.Contains(lower, "interview") || strings.Contains(lower, "anki") || strings.Contains(lower, "spaced repetition"):
		return 4
	default:
		return 2
	}
}

func inferDifficulty(s string) string {
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "advanced") || strings.Contains(lower, "interface") || strings.Contains(lower, "generics") || strings.Contains(lower, "gc"):
		return "Advanced"
	case strings.Contains(lower, "beginner") || strings.Contains(lower, "tour") || strings.Contains(lower, "effective go") || strings.Contains(lower, "go by example"):
		return "Beginner"
	case strings.Contains(lower, "performance") || strings.Contains(lower, "concurrency") || strings.Contains(lower, "sync.pool"):
		return "Intermediate"
	default:
		return "Intermediate"
	}
}

// CheckURLHealth hits the URL and returns whether it is reachable.
func CheckURLHealth(rawURL string) (bool, error) {
	url := normalizeURL(rawURL)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Head(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return true, nil
}

// ReplaceDeadURLWithArchive attempts to preserve a resource by redirecting to Wayback.
func ReplaceDeadURLWithArchive(rawURL string) string {
	alive, err := CheckURLHealth(rawURL)
	if err == nil && alive {
		return normalizeURL(rawURL)
	}
	return "https://web.archive.org/web/*/" + normalizeURL(rawURL)
}

// DefaultCatalog returns a starter resource catalog from the known learning groups.
func DefaultCatalog() ResourceSet {
	return ResourceSet{Resources: []Resource{
		{Title: "Tour of Go", URL: "https://tour.golang.org/", Category: "Core", Author: "Go Team", Phase: 1, Difficulty: "Beginner"},
		{Title: "Effective Go", URL: "https://go.dev/doc/effective_go", Category: "Core", Author: "Go Team", Phase: 1, Difficulty: "Beginner"},
		{Title: "Go by Example", URL: "https://gobyexample.com/", Category: "Core", Author: "Go Team", Phase: 1, Difficulty: "Beginner"},
		{Title: "Russ Cox on Interfaces", URL: "https://research.swtch.com/interfaces", Category: "Interfaces", Author: "Russ Cox", Phase: 3, Difficulty: "Advanced"},
		{Title: "Dave Cheney Blog", URL: "https://dave.cheney.net/", Category: "Performance", Author: "Dave Cheney", Phase: 3, Difficulty: "Intermediate"},
		{Title: "Go Slice Tricks", URL: "https://github.com/golang/go/wiki/GoSliceTricks", Category: "Reference", Author: "Go Team", Phase: 2, Difficulty: "Intermediate"},
	}}
}
