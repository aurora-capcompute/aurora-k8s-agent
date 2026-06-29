import type {
  JournalEntry,
  ManifestInfo,
  ProgressEvent,
  Resolution,
  RunGraphNode,
  RunSnapshot,
  TaskSnapshot,
  ThreadGraph,
  ThreadSummary,
} from "./types";

// API base; relative by default so the dev proxy (vite) and prod proxy (nginx)
// both forward /api to the agent. Override with VITE_API_BASE if needed.
const BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? "";

// Bearer token — loaded from localStorage at module init so the first API
// call is already authenticated without a separate initialization step.
// Wrapped in try/catch because localStorage can throw in restrictive contexts
// (private-browsing iframes, storage-blocked origins).
let _token = (() => {
  try {
    return localStorage.getItem("aurora_token") ?? "";
  } catch {
    return "";
  }
})();

export function setToken(token: string | null) {
  _token = token ?? "";
  try {
    if (token) {
      localStorage.setItem("aurora_token", token);
    } else {
      localStorage.removeItem("aurora_token");
    }
  } catch {
    // Ignore storage errors; token survives for the session in the module var.
  }
}

// Thrown when the server responds with 401 so callers can distinguish "needs
// login" from other errors.
export class UnauthorizedError extends Error {
  constructor() {
    super("unauthorized");
    this.name = "UnauthorizedError";
  }
}

function authHeaders(): Record<string, string> {
  return _token ? { Authorization: `Bearer ${_token}` } : {};
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`, { headers: authHeaders() });
  if (res.status === 401) throw new UnauthorizedError();
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  return res.json() as Promise<T>;
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...authHeaders() },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401) throw new UnauthorizedError();
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return (text ? JSON.parse(text) : {}) as T;
}

export const api = {
  // Exchange username/password for the channel bearer token.
  login: (name: string, password: string) =>
    post<{ token: string }>("/api/login", { name, password }),

  manifests: () => get<ManifestInfo[]>("/api/manifests"),
  manifestThreads: (name: string) =>
    get<ThreadSummary[]>(`/api/manifests/${encodeURIComponent(name)}/threads`),
  createThread: (name: string) =>
    post<{ id: string }>(`/api/manifests/${encodeURIComponent(name)}/threads`),
  threadGraph: (id: string) => get<ThreadGraph>(`/api/threads/${id}/graph`),
  runGraph: (id: string) => get<RunGraphNode>(`/api/runs/${id}/graph`),
  sendMessage: (threadID: string, message: string) =>
    post<RunSnapshot>(`/api/threads/${threadID}/messages`, { message }),
  retryRun: (runID: string, mode: "resume" | "restart") =>
    post<RunSnapshot>(`/api/runs/${runID}/retry`, { mode }),
  stopRun: (runID: string) => post<RunSnapshot>(`/api/runs/${runID}/stop`),
  journal: (runID: string) => get<JournalEntry[]>(`/api/runs/${runID}/journal`),
  journalRevisions: (runID: string) =>
    get<Record<string, JournalEntry[]>>(`/api/runs/${runID}/journal/revisions`),
  tasks: (runID: string) => get<TaskSnapshot[]>(`/api/runs/${runID}/tasks`),
  resolveTask: (taskID: string, token: string, resolution: Resolution) =>
    post<TaskSnapshot>(`/api/tasks/${taskID}/resolve`, { token, resolution }),
};

// subscribe opens the SSE event stream for a thread; returns an unsubscribe fn.
// The bearer token is passed as ?token= because browser EventSource cannot set
// custom headers.
// onProgress is called for each aurora.log progress line emitted during a run.
export function subscribe(
  threadID: string,
  onEvent: () => void,
  onProgress?: (runID: string, message: string) => void,
): () => void {
  const tokenParam = _token ? `?token=${encodeURIComponent(_token)}` : "";
  const es = new EventSource(
    `${BASE}/api/threads/${threadID}/events${tokenParam}`,
  );
  const handler = () => onEvent();
  es.onmessage = handler;
  for (const t of ["snapshot", "run.updated", "journal.appended", "task.created", "task.updated"]) {
    es.addEventListener(t, handler);
  }
  if (onProgress) {
    es.addEventListener("progress", (e) => {
      try {
        const ev = JSON.parse((e as MessageEvent).data) as {
          data?: ProgressEvent;
        };
        const { run_id, message } = ev.data ?? {};
        if (run_id && message) onProgress(run_id, message);
      } catch {
        // malformed event — ignore
      }
    });
  }
  return () => es.close();
}
