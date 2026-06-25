// Package webapi serves the agent's HTTP API: read-only execution-graph
// projections, interactive thread/run control, and a live event stream. It wraps
// the aurora.Runtime so a UI (or any client) can roam the run history and drive
// the agent the way a chat channel does.
package webapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"aurora-capcompute/aurora"
)

// Handler builds an http.Handler exposing the agent API under /api.
func Handler(runtime aurora.Runtime) http.Handler {
	h := &handler{runtime: runtime}
	mux := http.NewServeMux()

	// Read-only projections.
	mux.HandleFunc("GET /api/threads", h.listThreads)
	mux.HandleFunc("GET /api/threads/{id}", h.getThread)
	mux.HandleFunc("GET /api/threads/{id}/graph", h.threadGraph)
	mux.HandleFunc("GET /api/threads/{id}/events", h.threadEvents)
	mux.HandleFunc("GET /api/runs/{id}", h.getRun)
	mux.HandleFunc("GET /api/runs/{id}/graph", h.runGraph)
	mux.HandleFunc("GET /api/runs/{id}/journal", h.runJournal)
	mux.HandleFunc("GET /api/runs/{id}/tasks", h.runTasks)
	mux.HandleFunc("GET /api/brains", h.listBrains)

	// Interactive control (create threads, chat, steer runs, approve tasks).
	mux.HandleFunc("POST /api/threads", h.createThread)
	mux.HandleFunc("POST /api/threads/{id}/messages", h.sendMessage)
	mux.HandleFunc("POST /api/runs/{id}/stop", h.stopRun)
	mux.HandleFunc("POST /api/runs/{id}/retry", h.retryRun)
	mux.HandleFunc("POST /api/tasks/{id}/resolve", h.resolveTask)

	return mux
}

type handler struct {
	runtime aurora.Runtime
}

// --- read-only ---

func (h *handler) listThreads(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, h.runtime.ListThreads(), nil)
}

func (h *handler) getThread(w http.ResponseWriter, r *http.Request) {
	snap, err := h.runtime.GetThread(r.PathValue("id"))
	writeJSON(w, snap, err)
}

func (h *handler) threadGraph(w http.ResponseWriter, r *http.Request) {
	graph, err := h.runtime.ThreadGraph(r.PathValue("id"))
	writeJSON(w, graph, err)
}

func (h *handler) getRun(w http.ResponseWriter, r *http.Request) {
	snap, err := h.runtime.GetRun(r.PathValue("id"))
	writeJSON(w, snap, err)
}

func (h *handler) runGraph(w http.ResponseWriter, r *http.Request) {
	graph, err := h.runtime.CallGraph(r.PathValue("id"))
	writeJSON(w, graph, err)
}

func (h *handler) runJournal(w http.ResponseWriter, r *http.Request) {
	entries, err := h.runtime.Journal(r.PathValue("id"))
	writeJSON(w, entries, err)
}

func (h *handler) runTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.runtime.Tasks(r.PathValue("id"))
	writeJSON(w, tasks, err)
}

func (h *handler) listBrains(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, h.runtime.Brains(), nil)
}

// --- interactive ---

func (h *handler) createThread(w http.ResponseWriter, r *http.Request) {
	var manifest aurora.Manifest
	if !readJSON(w, r, &manifest) {
		return
	}
	snap, err := h.runtime.CreateThread(manifest)
	writeJSON(w, snap, err)
}

type messageRequest struct {
	Message   string                    `json:"message"`
	Overrides []aurora.CapabilityConfig `json:"overrides,omitempty"`
}

func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req messageRequest
	if !readJSON(w, r, &req) {
		return
	}
	run, err := h.runtime.CreateRun(r.PathValue("id"), req.Message, req.Overrides)
	writeJSON(w, run, err)
}

func (h *handler) stopRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.runtime.Stop(r.PathValue("id"))
	writeJSON(w, run, err)
}

type retryRequest struct {
	Mode      aurora.RetryMode          `json:"mode"`
	Overrides []aurora.CapabilityConfig `json:"overrides,omitempty"`
}

func (h *handler) retryRun(w http.ResponseWriter, r *http.Request) {
	var req retryRequest
	if !readJSON(w, r, &req) {
		return
	}
	run, err := h.runtime.Retry(r.PathValue("id"), req.Mode, req.Overrides)
	writeJSON(w, run, err)
}

type resolveRequest struct {
	Token      string            `json:"token"`
	Resolution aurora.Resolution `json:"resolution"`
}

func (h *handler) resolveTask(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if !readJSON(w, r, &req) {
		return
	}
	task, err := h.runtime.ResolveTask(r.PathValue("id"), req.Token, req.Resolution)
	writeJSON(w, task, err)
}

// --- live event stream (SSE) ---

func (h *handler) threadEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	initial, events, cancel, err := h.runtime.Subscribe(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeSSE(w, initial)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			writeSSE(w, event)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, event aurora.Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, payload)
}

// --- helpers ---

func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, payload any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if encodeErr := json.NewEncoder(w).Encode(payload); encodeErr != nil {
		http.Error(w, encodeErr.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), statusFor(err))
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, aurora.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, aurora.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, aurora.ErrConflict):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
