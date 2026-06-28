package webapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/binding"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/controller"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/webapi"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/webchannel"
)

// fakeRuntime embeds aurora.Runtime so it satisfies the interface; only the
// methods the API exercises are overridden.
type fakeRuntime struct {
	aurora.Runtime
	threads      []aurora.ThreadSummary
	thread       aurora.ThreadSnapshot
	run          aurora.RunSnapshot
	notFound     bool
	conflict     bool
	lastManifest aurora.Manifest
	lastMessage  string
	lastMode     aurora.RetryMode
	initial      aurora.Event
	events       chan aurora.Event
}

func (f *fakeRuntime) ListThreads() []aurora.ThreadSummary { return f.threads }

func (f *fakeRuntime) GetThread(id string) (aurora.ThreadSnapshot, error) {
	if f.notFound {
		return aurora.ThreadSnapshot{}, fmt.Errorf("%w: %s", aurora.ErrNotFound, id)
	}
	return f.thread, nil
}

func (f *fakeRuntime) CreateThread(m aurora.Manifest, tags map[string]string) (aurora.ThreadSnapshot, error) {
	f.lastManifest = m
	return f.thread, nil
}

func (f *fakeRuntime) CreateRun(_ string, msg string, _ []aurora.CapabilityConfig) (aurora.RunSnapshot, error) {
	f.lastMessage = msg
	return f.run, nil
}

func (f *fakeRuntime) GetRun(id string) (aurora.RunSnapshot, error) {
	if f.notFound {
		return aurora.RunSnapshot{}, fmt.Errorf("%w: %s", aurora.ErrNotFound, id)
	}
	return f.run, nil
}

func (f *fakeRuntime) Retry(_ string, mode aurora.RetryMode, _ []aurora.CapabilityConfig) (aurora.RunSnapshot, error) {
	if f.conflict {
		return aurora.RunSnapshot{}, fmt.Errorf("%w: cannot retry", aurora.ErrConflict)
	}
	f.lastMode = mode
	return f.run, nil
}

func (f *fakeRuntime) ReplayFrom(_ string, _ int) (aurora.RunSnapshot, error) {
	return f.run, nil
}

func (f *fakeRuntime) Subscribe(string) (aurora.Event, <-chan aurora.Event, func(), error) {
	return f.initial, f.events, func() {}, nil
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestReadAndInteractiveRoutes(t *testing.T) {
	fake := &fakeRuntime{
		threads: []aurora.ThreadSummary{{ID: "t1", Title: "first"}},
		thread:  aurora.ThreadSnapshot{ThreadSummary: aurora.ThreadSummary{ID: "t1"}},
		run:     aurora.RunSnapshot{ID: "r1", ThreadID: "t1"},
	}
	h := webapi.Handler(fake, nil)

	t.Run("list threads", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/threads", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var got []aurora.ThreadSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].ID != "t1" {
			t.Fatalf("threads = %s (err %v)", rec.Body.String(), err)
		}
	})

	t.Run("create thread", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/api/threads", `{"version":2,"brain":"kubernetes-agent"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if fake.lastManifest.Brain != "kubernetes-agent" {
			t.Fatalf("manifest not decoded: %+v", fake.lastManifest)
		}
	})

	t.Run("send message", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/api/threads/t1/messages", `{"message":"hello"}`)
		if rec.Code != http.StatusOK || fake.lastMessage != "hello" {
			t.Fatalf("status %d, message %q", rec.Code, fake.lastMessage)
		}
	})

	t.Run("retry with mode", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/api/runs/r1/retry", `{"mode":"restart"}`)
		if rec.Code != http.StatusOK || fake.lastMode != aurora.RetryRestart {
			t.Fatalf("status %d, mode %q", rec.Code, fake.lastMode)
		}
	})

	t.Run("bad body is 400", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/api/threads/t1/messages", `not json`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", rec.Code)
		}
	})
}

func TestErrorStatusMapping(t *testing.T) {
	t.Run("not found -> 404", func(t *testing.T) {
		h := webapi.Handler(&fakeRuntime{notFound: true}, nil)
		rec := do(t, h, http.MethodGet, "/api/threads/nope", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", rec.Code)
		}
	})
	t.Run("conflict -> 409", func(t *testing.T) {
		h := webapi.Handler(&fakeRuntime{conflict: true}, nil)
		rec := do(t, h, http.MethodPost, "/api/runs/r1/retry", `{"mode":"restart"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status %d, want 409", rec.Code)
		}
	})
}

func TestEventStreamSSE(t *testing.T) {
	ch := make(chan aurora.Event, 1)
	ch <- aurora.Event{Type: "run.updated"}
	close(ch)
	fake := &fakeRuntime{initial: aurora.Event{Type: "snapshot"}, events: ch}

	server := httptest.NewServer(webapi.Handler(fake, nil))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/threads/t1/events")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "event: snapshot") || !strings.Contains(string(body), "event: run.updated") {
		t.Fatalf("sse body missing events:\n%s", body)
	}
}

func TestManifestRoutes(t *testing.T) {
	manifest := aurora.Manifest{Version: aurora.ManifestVersion, Brain: "kubernetes-agent"}
	ch := webchannel.New()
	ch.Apply(controller.Resolved{Bindings: []controller.SourceBinding{{
		Source:   "web",
		Name:     "ops",
		Resolved: binding.Resolved{Manifest: manifest, Digest: controller.Digest(manifest)},
	}}}, nil)

	fake := &fakeRuntime{
		threads: []aurora.ThreadSummary{
			{ID: "t1", Manifest: aurora.Manifest{Version: aurora.ManifestVersion, Brain: "kubernetes-agent", BindingRef: "ops"}},
			{ID: "t2", Manifest: aurora.Manifest{Brain: "other"}}, // unrelated — no BindingRef
		},
		thread: aurora.ThreadSnapshot{ThreadSummary: aurora.ThreadSummary{ID: "new"}},
	}
	h := webapi.Handler(fake, ch)

	t.Run("list manifests", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/manifests", "")
		var got []webchannel.ManifestInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].Name != "ops" {
			t.Fatalf("manifests = %s (err %v)", rec.Body.String(), err)
		}
	})

	t.Run("threads for manifest", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/manifests/ops/threads", "")
		var got []aurora.ThreadSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].ID != "t1" {
			t.Fatalf("manifest threads = %s (err %v)", rec.Body.String(), err)
		}
	})

	t.Run("create thread under manifest", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/api/manifests/ops/threads", "")
		if rec.Code != http.StatusOK || fake.lastManifest.Brain != "kubernetes-agent" {
			t.Fatalf("status %d, manifest %+v", rec.Code, fake.lastManifest)
		}
	})

	t.Run("unknown manifest is 404", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/manifests/nope/threads", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", rec.Code)
		}
	})
}
