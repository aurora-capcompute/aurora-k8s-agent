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

func TestSourceKinds(t *testing.T) {
	cases := []struct {
		name    string
		sources string
		channel string
		want    []string
	}{
		{"default telegram", "", "", []string{"telegram"}},
		{"explicit list", "telegram,slack", "", []string{"telegram", "slack"}},
		{"none disables chat", "none", "", nil},
		{"dedupe and trim", "slack, slack ,telegram", "", []string{"slack", "telegram"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AURORA_SOURCES", tc.sources)
			t.Setenv("AURORA_CHANNEL", tc.channel)
			got := sourceKinds()
			if len(got) != len(tc.want) {
				t.Fatalf("sourceKinds() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sourceKinds() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestControlPlaneKind(t *testing.T) {
	t.Run("default none", func(t *testing.T) {
		t.Setenv("AURORA_CONTROL_PLANE", "")
		t.Setenv("AURORA_CONTROLLER", "")
		if got := controlPlaneKind(); got != "none" {
			t.Fatalf("controlPlaneKind() = %q, want none", got)
		}
	})
	t.Run("explicit fs", func(t *testing.T) {
		t.Setenv("AURORA_CONTROL_PLANE", "fs")
		if got := controlPlaneKind(); got != "fs" {
			t.Fatalf("controlPlaneKind() = %q, want fs", got)
		}
	})
	t.Run("legacy controller flag", func(t *testing.T) {
		t.Setenv("AURORA_CONTROL_PLANE", "")
		t.Setenv("AURORA_CONTROLLER", "true")
		if got := controlPlaneKind(); got != "k8s" {
			t.Fatalf("controlPlaneKind() = %q, want k8s", got)
		}
	})
}
