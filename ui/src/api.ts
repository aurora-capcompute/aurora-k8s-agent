import type {
  ManifestInfo,
  RunGraphNode,
  RunSnapshot,
  ThreadGraph,
  ThreadSummary,
} from "./types";

// API base; relative by default so the dev proxy (vite) and prod proxy (nginx)
// both forward /api to the agent. Override with VITE_API_BASE if needed.
const BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? "";

async function get<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`);
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  return res.json() as Promise<T>;
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return (text ? JSON.parse(text) : {}) as T;
}

export const api = {
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
};

// subscribe opens the SSE event stream for a thread; returns an unsubscribe fn.
export function subscribe(
  threadID: string,
  onEvent: (type: string) => void,
): () => void {
  const es = new EventSource(`${BASE}/api/threads/${threadID}/events`);
  const handler = (e: MessageEvent) => {
    try {
      const parsed = JSON.parse(e.data) as { type?: string };
      onEvent(parsed.type ?? "message");
    } catch {
      onEvent("message");
    }
  };
  es.onmessage = handler;
  // The agent labels events (event: <type>); listen for the common ones too.
  for (const t of ["snapshot", "run.updated", "journal.appended"]) {
    es.addEventListener(t, handler as EventListener);
  }
  return () => es.close();
}
