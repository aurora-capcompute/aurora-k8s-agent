package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aurora-capcompute/aurora"
)

// graphAPIFake embeds aurora.Runtime so it satisfies the interface; only the
// methods exercised by the graph API are overridden.
type graphAPIFake struct {
	aurora.Runtime
	threads []aurora.ThreadSummary
	graph   aurora.ThreadGraph
}

func (f graphAPIFake) ListThreads() []aurora.ThreadSummary { return f.threads }

func (f graphAPIFake) ThreadGraph(id string) (aurora.ThreadGraph, error) {
	if id != f.graph.ThreadID {
		return aurora.ThreadGraph{}, aurora.ErrNotFound
	}
	return f.graph, nil
}

func TestGraphAPIRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerGraphAPI(mux, graphAPIFake{
		threads: []aurora.ThreadSummary{{ID: "t1", Title: "first"}},
		graph:   aurora.ThreadGraph{ThreadID: "t1", Title: "first"},
	})

	t.Run("list threads", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/threads", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got []aurora.ThreadSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got) != 1 || got[0].ID != "t1" {
			t.Fatalf("threads = %+v", got)
		}
	})

	t.Run("thread graph", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/threads/t1/graph", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got aurora.ThreadGraph
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.ThreadID != "t1" {
			t.Fatalf("graph = %+v", got)
		}
	})

	t.Run("missing thread is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/threads/nope/graph", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}
