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
	"strings"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/webchannel"
)

// Handler builds an http.Handler exposing the agent API under /api. channel may
// be nil when the web channel is not enabled, in which case the manifest routes
// report 404.
func Handler(runtime aurora.Runtime, channel *webchannel.Channel) http.Handler {
	h := &handler{runtime: runtime, channel: channel}
	mux := http.NewServeMux()

	// Login: exchange username/password for the channel bearer token.
	mux.HandleFunc("POST /api/login", h.login)

	// Manifests bound to the web channel (the UI switcher) and their threads.
	mux.HandleFunc("GET /api/manifests", h.listManifests)
	mux.HandleFunc("GET /api/manifests/{name}/threads", h.manifestThreads)
	mux.HandleFunc("POST /api/manifests/{name}/threads", h.createManifestThread)

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
	mux.HandleFunc("POST /api/runs/{id}/replay", h.replayRun)
	mux.HandleFunc("POST /api/tasks/{id}/resolve", h.resolveTask)

	return mux
}

type handler struct {
	runtime aurora.Runtime
	channel *webchannel.Channel
}

// extractToken returns the bearer token from the Authorization header or, as a
// fallback for clients that cannot set headers (e.g. browser EventSource), from
// the ?token= query parameter.
func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// checkAccess validates the bearer token against the named binding's web-channel
// token. Returns true and proceeds when:
//   - no web channel is configured (open API), or
//   - the binding has no token requirement, or
//   - the provided bearer token matches.
//
// On failure it writes 401 and returns false.
func (h *handler) checkAccess(w http.ResponseWriter, r *http.Request, bindingRef string) bool {
	if h.channel == nil {
		return true
	}
	if h.channel.HasAccess(bindingRef, extractToken(r)) {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

// threadAccess fetches the thread and validates access in one step.
func (h *handler) threadAccess(w http.ResponseWriter, r *http.Request, threadID string) (aurora.ThreadSnapshot, bool) {
	snap, err := h.runtime.GetThread(threadID)
	if err != nil {
		writeError(w, err)
		return aurora.ThreadSnapshot{}, false
	}
	if !h.checkAccess(w, r, snap.Manifest.BindingRef) {
		return aurora.ThreadSnapshot{}, false
	}
	return snap, true
}

// runAccess fetches the run and validates access via its thread in one step.
func (h *handler) runAccess(w http.ResponseWriter, r *http.Request, runID string) (aurora.RunSnapshot, bool) {
	run, err := h.runtime.GetRun(runID)
	if err != nil {
		writeError(w, err)
		return aurora.RunSnapshot{}, false
	}
	thread, err := h.runtime.GetThread(run.ThreadID)
	if err != nil {
		writeError(w, err)
		return aurora.RunSnapshot{}, false
	}
	if !h.checkAccess(w, r, thread.Manifest.BindingRef) {
		return aurora.RunSnapshot{}, false
	}
	return run, true
}

// --- login ---

type loginRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

// login is public (no bearer required). It validates the credentials against
// the web channel's user list and returns the channel bearer token on success.
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if h.channel == nil {
		http.Error(w, "web channel not enabled", http.StatusNotFound)
		return
	}
	var req loginRequest
	if !readJSON(w, r, &req) {
		return
	}
	tok, ok := h.channel.Login(req.Name, req.Password)
	if !ok {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	writeJSON(w, loginResponse{Token: string(tok)}, nil)
}

// --- manifests bound to the web channel ---

func (h *handler) listManifests(w http.ResponseWriter, r *http.Request) {
	if h.channel == nil {
		writeJSON(w, []webchannel.ManifestInfo{}, nil)
		return
	}
	tok := extractToken(r)
	all := h.channel.Manifests()
	out := make([]webchannel.ManifestInfo, 0, len(all))
	needsAuth := false
	for _, m := range all {
		if h.channel.HasAccess(m.Name, tok) {
			out = append(out, m)
		} else {
			needsAuth = true
		}
	}
	// If every manifest required a token and none matched, the caller is not
	// authenticated rather than the list being empty.
	if len(out) == 0 && needsAuth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, out, nil)
}

func (h *handler) manifestThreads(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if h.channel == nil || !h.channel.Has(name) {
		http.Error(w, "manifest not found", http.StatusNotFound)
		return
	}
	if !h.checkAccess(w, r, name) {
		return
	}
	threads := make([]aurora.ThreadSummary, 0)
	for _, t := range h.runtime.ListThreads() {
		if t.Manifest.BindingRef == name {
			threads = append(threads, t)
		}
	}
	writeJSON(w, threads, nil)
}

func (h *handler) createManifestThread(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if h.channel == nil {
		http.Error(w, "web channel not enabled", http.StatusNotFound)
		return
	}
	manifest, ok := h.channel.Manifest(name)
	if !ok {
		http.Error(w, "manifest not found", http.StatusNotFound)
		return
	}
	if !h.checkAccess(w, r, name) {
		return
	}
	snap, err := h.runtime.CreateThread(manifest, nil)
	writeJSON(w, snap, err)
}

// --- read-only ---

func (h *handler) listThreads(w http.ResponseWriter, r *http.Request) {
	all := h.runtime.ListThreads()
	if h.channel == nil {
		writeJSON(w, all, nil)
		return
	}
	tok := extractToken(r)
	filtered := make([]aurora.ThreadSummary, 0, len(all))
	for _, t := range all {
		if h.channel.HasAccess(t.Manifest.BindingRef, tok) {
			filtered = append(filtered, t)
		}
	}
	writeJSON(w, filtered, nil)
}

func (h *handler) getThread(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.threadAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, snap, nil)
}

func (h *handler) threadGraph(w http.ResponseWriter, r *http.Request) {
	snap, ok := h.threadAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	graph, err := h.runtime.ThreadGraph(snap.ID)
	writeJSON(w, graph, err)
}

func (h *handler) getRun(w http.ResponseWriter, r *http.Request) {
	run, ok := h.runAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, run, nil)
}

func (h *handler) runGraph(w http.ResponseWriter, r *http.Request) {
	run, ok := h.runAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	graph, err := h.runtime.CallGraph(run.ID)
	writeJSON(w, graph, err)
}

func (h *handler) runJournal(w http.ResponseWriter, r *http.Request) {
	run, ok := h.runAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	entries, err := h.runtime.Journal(run.ID)
	writeJSON(w, entries, err)
}

func (h *handler) runTasks(w http.ResponseWriter, r *http.Request) {
	run, ok := h.runAccess(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	tasks, err := h.runtime.Tasks(run.ID)
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
	if !h.checkAccess(w, r, manifest.BindingRef) {
		return
	}
	snap, err := h.runtime.CreateThread(manifest, nil)
	writeJSON(w, snap, err)
}

type messageRequest struct {
	Message   string                    `json:"message"`
	Overrides []aurora.CapabilityConfig `json:"overrides,omitempty"`
}

func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("id")
	snap, ok := h.threadAccess(w, r, threadID)
	if !ok {
		return
	}
	var req messageRequest
	if !readJSON(w, r, &req) {
		return
	}
	run, err := h.runtime.CreateRun(snap.ID, req.Message, req.Overrides)
	writeJSON(w, run, err)
}

func (h *handler) stopRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, ok := h.runAccess(w, r, runID); !ok {
		return
	}
	run, err := h.runtime.Stop(runID)
	writeJSON(w, run, err)
}

type retryRequest struct {
	Mode      aurora.RetryMode          `json:"mode"`
	Overrides []aurora.CapabilityConfig `json:"overrides,omitempty"`
}

func (h *handler) retryRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, ok := h.runAccess(w, r, runID); !ok {
		return
	}
	var req retryRequest
	if !readJSON(w, r, &req) {
		return
	}
	run, err := h.runtime.Retry(runID, req.Mode, req.Overrides)
	writeJSON(w, run, err)
}

type replayRequest struct {
	From int `json:"from"`
}

func (h *handler) replayRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, ok := h.runAccess(w, r, runID); !ok {
		return
	}
	var req replayRequest
	if !readJSON(w, r, &req) {
		return
	}
	run, err := h.runtime.ReplayFrom(runID, req.From)
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
	threadID := r.PathValue("id")
	if _, ok := h.threadAccess(w, r, threadID); !ok {
		return
	}
	initial, events, cancel, err := h.runtime.Subscribe(threadID)
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
