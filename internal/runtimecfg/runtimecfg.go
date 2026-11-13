// Package runtimecfg serves the versioned runtime config document that every API instance polls.
//
// A move changes the shop's behaviour by patching this document: replica routing percentages,
// cache switches, backends. Each patch bumps the version; instances export the version they run,
// and the architect waits for every one to report it before its next step (a convergence barrier).
package runtimecfg

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Store holds the document and persists every version.
type Store struct {
	mu   sync.Mutex
	path string
	doc  map[string]any
}

// Open loads the document from path, or starts at version 0 with no overrides. The API merges
// the document over its own defaults, so version 0 means genesis behaviour.
func Open(path string) (*Store, error) {
	s := &Store{path: path, doc: map[string]any{"version": 0}}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(data, &s.doc)
}

// Version is the current version.
func (s *Store) Version() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return version(s.doc)
}

func version(doc map[string]any) int {
	switch v := doc["version"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// Doc returns a copy of the document.
func (s *Store) Doc() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.doc)
}

func clone(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	json.Unmarshal(data, &out)
	return out
}

// Merge deep-merges patch into base.
func Merge(base, patch map[string]any) map[string]any {
	out := clone(base)
	for k, v := range patch {
		if pm, ok := v.(map[string]any); ok {
			if bm, ok := out[k].(map[string]any); ok {
				out[k] = Merge(bm, pm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// Patch applies a change and bumps the version. It returns the new version.
func (s *Store) Patch(patch map[string]any) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := Merge(s.doc, patch)
	v := version(s.doc) + 1
	next["version"] = v
	if s.path != "" {
		data, err := json.MarshalIndent(next, "", " ")
		if err != nil {
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			return 0, err
		}
		tmp := s.path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return 0, err
		}
		if err := os.Rename(tmp, s.path); err != nil {
			return 0, err
		}
	}
	s.doc = next
	return v, nil
}

// ServeHTTP answers GET with the document and an ETag, and 304 when the instance has it already.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data, _ := json.Marshal(s.doc)
	etag := fmt.Sprintf(`"v%d"`, version(s.doc))
	s.mu.Unlock()
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
