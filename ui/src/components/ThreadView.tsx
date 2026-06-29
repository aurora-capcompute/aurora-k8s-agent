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
  onRunSelect,
  onDrawerClose,
  onUnauthorized,
  onReloadThreads,
}: {
  threadID: string;
  drawerOpen: boolean;
  drawerRunID: string | null;
  onToggleDrawer: () => void;
  onRunClick: (runID: string) => void;
  onRunSelect: (runID: string) => void;
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
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const onReloadThreadsRef = useRef(onReloadThreads);
  useEffect(() => { onReloadThreadsRef.current = onReloadThreads; }, [onReloadThreads]);

  // Stable refs for hotkey handler (no re-registration on prop/state change).
  const rootRunsRef = useRef<{ run_id: string }[]>([]);
  const activeDrawerRunIDRef = useRef<string | null>(null);
  const drawerOpenRef = useRef(drawerOpen);
  drawerOpenRef.current = drawerOpen;
  const onRunClickRef = useRef(onRunClick);
  onRunClickRef.current = onRunClick;
  const onRunSelectRef = useRef(onRunSelect);
  onRunSelectRef.current = onRunSelect;

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
        onReloadThreadsRef.current?.();
      },
      onProgress,
    );
    return unsubscribe;
  }, [threadID, reload, onProgress]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [graph]);

  // `d` toggles the debug drawer; guard against firing while typing
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key !== "d" || e.ctrlKey || e.metaKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
      onToggleDrawer();
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [onToggleDrawer]);

  // `/` focuses the composer; `j`/`k` navigates runs when drawer is closed.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      const typing = !!(t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable));

      if (e.key === "/" && !typing) {
        e.preventDefault();
        inputRef.current?.focus();
        return;
      }
      if (typing) return;

      if ((e.key === "j" || e.key === "k") && !drawerOpenRef.current) {
        const runs = rootRunsRef.current;
        if (runs.length === 0) return;
        const cur = activeDrawerRunIDRef.current;
        const idx = cur ? runs.findIndex((r) => r.run_id === cur) : -1;
        const next = e.key === "j" ? idx + 1 : idx - 1;
        const chosen = runs[Math.max(0, Math.min(runs.length - 1, next))];
        if (chosen) onRunSelectRef.current(chosen.run_id);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []); // stable: all mutable values accessed through refs

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

  // Default the drawer to the most recent root run when none is explicitly selected.
  const rootRuns = (graph?.runs ?? []).filter((r) => !r.parent_run_id);
  const activeDrawerRunID =
    drawerRunID ?? (rootRuns.length > 0 ? rootRuns[rootRuns.length - 1].run_id : null);

  // Keep refs current for the stable hotkey handler.
  rootRunsRef.current = rootRuns;
  activeDrawerRunIDRef.current = activeDrawerRunID;

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
            {rootRuns.length === 0 && (
              <div className="transcript-empty">Send a message to start.</div>
            )}
            {rootRuns.map((run) => (
              <div key={run.run_id} className={`exchange${run.run_id === activeDrawerRunID ? " active" : ""}`}>
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
              ref={inputRef}
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
