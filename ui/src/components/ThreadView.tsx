import { useCallback, useEffect, useRef, useState } from "react";
import { api, subscribe, UnauthorizedError } from "../api";
import type { RunStatus, ThreadGraph } from "../types";
import { DebugDrawer } from "./DebugDrawer";

const TERMINAL: ReadonlySet<RunStatus> = new Set([
  "completed",
  "stopped",
  "failed",
  "interrupted",
]);

function StatusBadge({ status }: { status: RunStatus }) {
  return <span className={`badge badge-${status}`}>{status}</span>;
}

export function ThreadView({
  threadID,
  drawerOpen,
  drawerRunID,
  onToggleDrawer,
  onRunClick,
  onDrawerClose,
  onUnauthorized,
  onReloadThreads,
}: {
  threadID: string;
  drawerOpen: boolean;
  drawerRunID: string | null;
  onToggleDrawer: () => void;
  onRunClick: (runID: string) => void;
  onDrawerClose: () => void;
  onUnauthorized?: () => void;
  onReloadThreads?: () => void;
}) {
  const [graph, setGraph] = useState<ThreadGraph | null>(null);
  const [tick, setTick] = useState(0);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [progress, setProgress] = useState<Map<string, string[]>>(new Map());
  const bottomRef = useRef<HTMLDivElement>(null);

  const handleError = useCallback(
    (e: unknown) => {
      if (e instanceof UnauthorizedError) onUnauthorized?.();
      else setError(String(e));
    },
    [onUnauthorized],
  );

  const reload = useCallback(async () => {
    try {
      const g = await api.threadGraph(threadID);
      setGraph(g);
      setError(null);
    } catch (e) {
      handleError(e);
    }
  }, [threadID, handleError]);

  const onProgress = useCallback((runID: string, message: string) => {
    setProgress((prev) => {
      const lines = prev.get(runID) ?? [];
      const next = new Map(prev);
      next.set(runID, [...lines.slice(-19), message]);
      return next;
    });
  }, []);

  // Clear progress lines for completed runs.
  useEffect(() => {
    const done = (graph?.runs ?? [])
      .filter((r) => TERMINAL.has(r.status))
      .map((r) => r.run_id);
    if (done.length === 0) return;
    setProgress((prev) => {
      if (!done.some((id) => prev.has(id))) return prev;
      const next = new Map(prev);
      done.forEach((id) => next.delete(id));
      return next;
    });
  }, [graph]);

  useEffect(() => {
    setGraph(null);
    setProgress(new Map());
    void reload();
    const unsubscribe = subscribe(
      threadID,
      () => {
        setTick((t) => t + 1);
        void reload();
        onReloadThreads?.();
      },
      onProgress,
    );
    return unsubscribe;
  }, [threadID, reload, onProgress, onReloadThreads]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [graph]);

  const send = async () => {
    const message = input.trim();
    if (!message) return;
    setBusy(true);
    setInput("");
    try {
      await api.sendMessage(threadID, message);
      await reload();
      onReloadThreads?.();
    } catch (e) {
      handleError(e);
    } finally {
      setBusy(false);
    }
  };

  const title =
    graph?.title && graph.title !== "New thread"
      ? graph.title
      : threadID.slice(0, 20);

  // Default the drawer to the most recent run when none is explicitly selected.
  const runs = graph?.runs ?? [];
  const activeDrawerRunID =
    drawerRunID ?? (runs.length > 0 ? runs[runs.length - 1].run_id : null);

  return (
    <div className="thread-shell">
      <div className="thread-header">
        <span className="thread-header-title">{title}</span>
        <button
          className={`debug-toggle${drawerOpen ? " active" : ""}`}
          onClick={onToggleDrawer}
        >
          Debug
        </button>
      </div>

      <div className="thread-body">
        <div className="transcript">
          <div className="transcript-inner">
            {error && <div className="error">{error}</div>}
            {(graph?.runs ?? []).length === 0 && (
              <div className="transcript-empty">Send a message to start.</div>
            )}
            {(graph?.runs ?? []).map((run) => (
              <div key={run.run_id} className="exchange">
                <div className="msg user">{run.message}</div>

                {!TERMINAL.has(run.status) &&
                  (progress.get(run.run_id)?.length ?? 0) > 0 && (
                    <div className="progress-block">
                      <div className="progress-header">Working…</div>
                      {(progress.get(run.run_id) ?? []).map((line, i) => (
                        <div key={i} className="progress-line">
                          {line}
                        </div>
                      ))}
                    </div>
                  )}

                {run.answer && (
                  <div className="msg assistant">{run.answer}</div>
                )}
                {run.error && (
                  <div className="msg error-msg">⚠ {run.error}</div>
                )}

                <div className="run-meta">
                  <button
                    className="link"
                    onClick={() => onRunClick(run.run_id)}
                  >
                    {run.run_id.slice(0, 16)}
                  </button>
                  <StatusBadge status={run.status} />
                  <span className="rev-tag">r{run.current_revision}</span>
                </div>
              </div>
            ))}
            <div ref={bottomRef} />
          </div>
        </div>

        <div className="composer-wrap">
          <div className="composer">
            <textarea
              value={input}
              placeholder="Message…"
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  void send();
                }
              }}
            />
            <button disabled={busy} onClick={() => void send()}>
              {busy ? "…" : "Send"}
            </button>
          </div>
        </div>
      </div>

      <DebugDrawer
        runID={activeDrawerRunID}
        open={drawerOpen}
        tick={tick}
        onClose={onDrawerClose}
        onChanged={() => void reload()}
        onUnauthorized={onUnauthorized}
      />
    </div>
  );
}
