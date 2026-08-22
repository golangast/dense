package knowledge

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestResourceFilters(t *testing.T) {
	catalog := ResourceSet{Resources: []Resource{
		{Title: "Tour of Go", URL: "https://tour.golang.org/", Category: "Core", Author: "Go Team", Phase: 1, Difficulty: "Beginner"},
		{Title: "Russ Cox Interfaces", URL: "https://example.com/interfaces", Category: "Interfaces", Author: "Russ Cox", Phase: 3, Difficulty: "Advanced"},
		{Title: "sync.Pool", URL: "https://example.com/pool", Category: "Concurrency", Author: "Dave Cheney", Phase: 2, Difficulty: "Intermediate"},
	}}

	if got := catalog.ByPhase(1); len(got) != 1 {
		t.Fatalf("expected 1 phase-1 resource, got %d", len(got))
	}
	if got := catalog.FilterByAuthor("Russ Cox"); len(got) != 1 {
		t.Fatalf("expected 1 russ cox resource, got %d", len(got))
	}
	if got := catalog.FilterByCategory("Concurrency"); len(got) != 1 {
		t.Fatalf("expected 1 concurrency resource, got %d", len(got))
	}
}

func TestExtractResourcesFromDOCX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.docx")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create docx: %v", err)
	}
	zw := zip.NewWriter(f)

	writeFile := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}

	writeFile("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`)
	writeFile("_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`)
	writeFile("word/document.xml", `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Concurrency</w:t></w:r></w:p><w:p><w:r><w:t>Rob Pike - https://go.dev/blog/concurrency-is-not-parallelism</w:t></w:r></w:p><w:p><w:r><w:t>Dave Cheney - https://dave.cheney.net/2017/07/11/why-go</w:t></w:r></w:p></w:body></w:document>`)
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	resources, err := ExtractResourcesFromDOCX(path)
	if err != nil {
		t.Fatalf("extract from docx: %v", err)
	}
	if len(resources) < 2 {
		t.Fatalf("expected at least 2 extracted resources, got %d", len(resources))
	}
	if resources[0].Category == "" {
		t.Fatalf("expected category set for first resource: %#v", resources[0])
	}
}
