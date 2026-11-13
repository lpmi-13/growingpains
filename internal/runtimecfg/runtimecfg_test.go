package runtimecfg

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPatchBumpsVersionAndPersists(t *testing.T) {
	path := t.TempDir() + "/cfg.json"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Patch(map[string]any{"read_routing": map[string]any{"replica_pct": 10, "replicas": []any{"a"}}}); v != 1 {
		t.Fatalf("version %d", v)
	}
	if v, _ := s.Patch(map[string]any{"read_routing": map[string]any{"replica_pct": 50}}); v != 2 {
		t.Fatalf("version %d", v)
	}
	rr := s.Doc()["read_routing"].(map[string]any)
	if rr["replica_pct"].(float64) != 50 || len(rr["replicas"].([]any)) != 1 {
		t.Fatalf("a patch lost a sibling key: %v", rr)
	}
	again, _ := Open(path)
	if again.Version() != 2 {
		t.Fatal("the document did not persist")
	}
}

func TestETag(t *testing.T) {
	s, _ := Open("")
	s.Patch(map[string]any{"cache": map[string]any{"read_pct": 10}})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))
	etag := rec.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("got %d for an unchanged document", rec.Code)
	}
}
