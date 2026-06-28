package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/policy"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/state"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/telegram"
	"github.com/aurora-capcompute/capcompute/dispatcher"
)

type flowProvider struct{}

func (flowProvider) Normalize(_ string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return raw, nil
}

func (flowProvider) NewDispatcher(
	context.Context,
	aurora.RunContext,
	aurora.Manifest,
) (dispatcher.Dispatcher[aurora.RunContext], error) {
	return nil, nil
}

func (flowProvider) IsSubset(string, json.RawMessage, json.RawMessage) error { return nil }

type flowRuntime struct {
	mu      sync.Mutex
	thread  aurora.ThreadSnapshot
	created bool
}

func (r *flowRuntime) CreateThread(manifest aurora.Manifest, tags map[string]string) (aurora.ThreadSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.thread = aurora.ThreadSnapshot{ThreadSummary: aurora.ThreadSummary{
		ID: "thread-1", Manifest: manifest,
	}}
	return r.thread, nil
}

func (r *flowRuntime) ListThreads() []aurora.ThreadSummary { return nil }
func (r *flowRuntime) Brains() []aurora.BrainArtifact      { return nil }
func (r *flowRuntime) SetBrains(context.Context, []aurora.BrainSource) error {
	return nil
}
func (r *flowRuntime) GetThread(string) (aurora.ThreadSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.thread, nil
}

func (r *flowRuntime) CreateRun(
	threadID, message string,
	overrides []aurora.CapabilityConfig,
) (aurora.RunSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = true
	r.thread.ActiveRunID = "run-1"
	return aurora.RunSnapshot{
		ID: "run-1", ThreadID: threadID, Message: message, Status: aurora.RunQueued,
	}, nil
}

func (r *flowRuntime) GetRun(string) (aurora.RunSnapshot, error) {
	return aurora.RunSnapshot{ID: "run-1", Status: aurora.RunQueued}, nil
}
func (r *flowRuntime) Journal(string) ([]aurora.JournalEntry, error) { return nil, nil }
func (r *flowRuntime) CallGraph(string) (aurora.RunGraphNode, error) {
	return aurora.RunGraphNode{}, nil
}
func (r *flowRuntime) ThreadGraph(string) (aurora.ThreadGraph, error) {
	return aurora.ThreadGraph{}, nil
}
func (r *flowRuntime) Tasks(string) ([]aurora.TaskSnapshot, error) { return nil, nil }
func (r *flowRuntime) ResolveTask(string, string, aurora.Resolution) (aurora.TaskSnapshot, error) {
	return aurora.TaskSnapshot{}, nil
}
func (r *flowRuntime) Stop(string) (aurora.RunSnapshot, error) {
	return aurora.RunSnapshot{Status: aurora.RunStopped}, nil
}
func (r *flowRuntime) Retry(string, aurora.RetryMode, []aurora.CapabilityConfig) (aurora.RunSnapshot, error) {
	return aurora.RunSnapshot{}, nil
}
func (r *flowRuntime) Subscribe(string) (aurora.Event, <-chan aurora.Event, func(), error) {
	ch := make(chan aurora.Event)
	return aurora.Event{}, ch, func() { close(ch) }, nil
}
func (r *flowRuntime) Close(context.Context) error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestPromptCreatesRun(t *testing.T) {
	policies, err := policy.Parse([]byte(`{
	  "version":1,
	  "users":{
	    "42":{
	      "allowed_chats":[42],
	      "manifest":{"version":2,"brain":"kubernetes-agent","capabilities":[{"name":"k8s.get"}]}
	    }
	  }
	}`), flowProvider{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var mu sync.Mutex
	nextMessageID := int64(10)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(request.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		mu.Lock()
		nextMessageID++
		id := nextMessageID
		mu.Unlock()
		result := any(true)
		if strings.HasSuffix(request.URL.Path, "sendMessage") {
			result = map[string]any{
				"message_id": id,
				"chat":       map[string]any{"id": 42, "type": "private"},
				"text":       payload["text"],
			}
		}
		response, _ := json.Marshal(map[string]any{"ok": true, "result": result})
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(response))),
		}, nil
	})
	client := telegram.NewClient("test")
	client.SetBaseURL("https://telegram.test")
	client.SetHTTPClient(&http.Client{Transport: transport})
	runtime := &flowRuntime{}
	service := New(runtime, client, store, policies,
		telegram.BotIdentity{ID: 99, Username: "aurora_bot"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer service.unsubscribeAll()

	ctx := context.Background()
	if err := service.handleUpdate(ctx, telegram.Update{Message: &telegram.Message{
		MessageID: 1, From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42, Type: "private"},
		Text: "list pods in default",
	}}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.created {
		t.Fatal("run was not created")
	}
}
