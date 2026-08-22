package dense

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const defaultCacheDir = ".dense"
const defaultCacheFile = "cache.json"

// WorkspaceCache is a lightweight serialisable snapshot of the workspace graph
// used to skip full re-indexing when the codebase hasn't changed.
type WorkspaceCache struct {
	RootPath  string            `json:"root_path"`
	IndexedAt time.Time         `json:"indexed_at"`
	// SymbolMap maps bare symbol name -> source file path for fast lookup.
	SymbolMap map[string]string `json:"symbol_map"`
	// KindMap maps bare symbol name -> kind ("func", "struct", …).
	KindMap   map[string]string `json:"kind_map"`
	// PkgMap maps bare symbol name -> full package import path.
	PkgMap    map[string]string `json:"pkg_map"`
}

// DefaultCachePath returns the standard cache file location relative to root.
func DefaultCachePath(rootPath string) string {
	return filepath.Join(rootPath, defaultCacheDir, defaultCacheFile)
}

// SaveWorkspaceCache serialises a WorkspaceCache to disk as indented JSON.
func SaveWorkspaceCache(cachePath string, cache *WorkspaceCache) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	return os.WriteFile(cachePath, data, 0644)
}

// LoadWorkspaceCache reads and deserialises a persisted WorkspaceCache.
// Returns nil (no error) when the file does not exist.
func LoadWorkspaceCache(cachePath string) (*WorkspaceCache, error) {
	data, err := os.ReadFile(cachePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}
	var cache WorkspaceCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("unmarshal cache: %w", err)
	}
	return &cache, nil
}

// GraphToCache converts a live WorkspaceGraph into a serialisable WorkspaceCache.
func GraphToCache(rootPath string, graph *WorkspaceGraph) *WorkspaceCache {
	cache := &WorkspaceCache{
		RootPath:  rootPath,
		IndexedAt: time.Now(),
		SymbolMap: make(map[string]string, len(graph.Symbols)),
		KindMap:   make(map[string]string, len(graph.Symbols)),
		PkgMap:    make(map[string]string, len(graph.Symbols)),
	}
	for _, sym := range graph.Symbols {
		cache.SymbolMap[sym.Name] = sym.FilePath
		cache.KindMap[sym.Name] = sym.Kind
		cache.PkgMap[sym.Name] = sym.Package
	}
	return cache
}

// FindInCache is a lightweight symbol lookup that doesn't require a live graph.
// Returns (filePath, kind, pkgPath, found).
func (c *WorkspaceCache) FindInCache(name string) (filePath, kind, pkgPath string, found bool) {
	if c == nil {
		return "", "", "", false
	}
	fp, ok := c.SymbolMap[name]
	if !ok {
		return "", "", "", false
	}
	return fp, c.KindMap[name], c.PkgMap[name], true
}

// Stale returns true when the cache is older than maxAge.
func (c *WorkspaceCache) Stale(maxAge time.Duration) bool {
	if c == nil {
		return true
	}
	return time.Since(c.IndexedAt) > maxAge
}
