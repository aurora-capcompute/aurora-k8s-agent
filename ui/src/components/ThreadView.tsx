import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, subscribe, UnauthorizedError } from "../api";
import type { RunStatus, ThreadGraph } from "../types";

const TERMINAL: ReadonlySet<RunStatus> = new Set([
  "completed",
  "stopped",
  "failed",
  "interrupted",
]);
import { CallGraph } from "./CallGraph";
import { RunPanel } from "./RunPanel";

type Tab = "chat" | "graph" | "revisions";

function StatusBadge({ status }: { status: RunStatus }) {
  return <span className={`badge badge-${status}`}>{status}</span>;
}

export function ThreadView({
  threadID,
  onUnauthorized,
}: {
  threadID: string;
  onUnauthorized?: () => void;
}) {
  const [graph, setGraph] = useState<ThreadGraph | null>(null);
  const [selectedRun, setSelectedRun] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("chat");
  const [tick, setTick] = useState(0);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [progress, setProgress] = useState<Map<string, string[]>>(new Map());
  const bottomRef = useRef<HTMLDivElement>(null);

  const handleError = useCallback(
    (e: unknown) => {
      if (e instanceof UnauthorizedError) {
        onUnauthorized?.();
      } else {
        setError(String(e));
      }
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

  // Clear progress lines for runs that have reached a terminal state.
  useEffect(() => {
    const runs = graph?.runs ?? [];
    const done = runs.filter((r) => TERMINAL.has(r.status)).map((r) => r.run_id);
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
    setSelectedRun(null);
    setProgress(new Map());
    void reload();
    const unsubscribe = subscribe(
      threadID,
      () => {
        setTick((t) => t + 1);
        void reload();
      },
      onProgress,
    );
    return unsubscribe;
  }, [threadID, reload, onProgress]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [graph]);

  const focusRun = useMemo(() => {
    if (selectedRun) return selectedRun;
    const runs = graph?.runs ?? [];
    return runs[runs.length - 1]?.run_id ?? null;
  }, [selectedRun, graph]);

  const focusEntries = useMemo(() => {
    if (!graph || !focusRun) return null;
    const run = (graph.runs ?? []).find((r) => r.run_id === focusRun);
    return run?.entries ?? null;
  }, [graph, focusRun]);

  const send = async () => {
    const message = input.trim();
    if (!message) return;
    setBusy(true);
    setInput("");
    try {
      await api.sendMessage(threadID, message);
      await reload();
    } catch (e) {
      handleError(e);
    } finally {
      setBusy(false);
    }
  };

  const inspect = (runID: string) => {
    setSelectedRun(runID);
    setTab("graph");
  };

  return (
    <div className="thread">
      <div className="tabs">
        {(["chat", "graph", "revisions"] as Tab[]).map((t) => (
          <button
            key={t}
            className={tab === t ? "tab active" : "tab"}
            onClick={() => setTab(t)}
          >
            {t}
          </button>
        ))}
        <span className="thread-id">{threadID}</span>
      </div>

      {error && <div className="error">{error}</div>}

      {tab === "chat" && (
        <>
          <div className="transcript">
            {(graph?.runs ?? []).map((run) => (
              <div key={run.run_id} className="exchange">
                <div className="msg user">{run.message}</div>
                {!TERMINAL.has(run.status) &&
                  (progress.get(run.run_id)?.length ?? 0) > 0 && (
                    <div className="msg progress-msg">
                      <div className="progress-header">Working…</div>
                      {(progress.get(run.run_id) ?? []).map((line, i) => (
                        <div key={i} className="progress-line">
                          {line}
                        </div>
                      ))}
                    </div>
                  )}
                {run.answer && <div className="msg assistant">{run.answer}</div>}
                {run.error && <div className="msg error-msg">⚠ {run.error}</div>}
                <div className="run-meta">
                  <button className="link" onClick={() => inspect(run.run_id)}>
                    {run.run_id}
                  </button>{" "}
                  <StatusBadge status={run.status} /> · rev{" "}
                  {run.current_revision}
                </div>
              </div>
            ))}
            <div ref={bottomRef} />
          </div>
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
        </>
      )}

      {tab === "graph" &&
        (graph && (graph.runs?.length ?? 0) > 0 ? (
          <div className="graph-wrap">
            <div className="graph-split">
              <div className="graph-canvas">
                <CallGraph entries={focusEntries ?? []} />
              </div>
              {focusRun && (
                <div className="graph-side">
                  <RunPanel
                    runID={focusRun}
                    tick={tick}
                    onChanged={() => void reload()}
                    onUnauthorized={onUnauthorized}
                  />
                </div>
              )}
            </div>
          </div>
        ) : (
          <div className="empty">No runs yet.</div>
        ))}

      {tab === "revisions" && (
        <div className="revisions">
          {(graph?.runs ?? []).map((run) => (
            <div key={run.run_id} className="rev-run">
              <div className="rev-run-head">
                <button className="link" onClick={() => inspect(run.run_id)}>
                  {run.run_id}
                </button>{" "}
                <StatusBadge status={run.status} /> — {run.message}
              </div>
              <ol className="journal">
                {(run.entries ?? []).map((entry) => (
                  <li
                    key={`${entry.index}-${entry.revision}`}
                    className={`outcome-${entry.outcome.status}`}
                  >
                    <details>
                      <summary>
                        <span className={`badge badge-${entry.outcome.status}`}>
                          {entry.outcome.status}
                        </span>
                        <code>{entry.call.name}</code>
                        <span className="rev-tag">r{entry.revision}</span>
                        {entry.outcome.message
                          ? ` — ${entry.outcome.message}`
                          : ""}
                      </summary>
                      {entry.call.args !== undefined && (
                        <pre className="json">
                          {JSON.stringify(entry.call.args, null, 2)}
                        </pre>
                      )}
                      {entry.outcome.result !== undefined && (
                        <pre className="json result">
                          {JSON.stringify(entry.outcome.result, null, 2)}
                        </pre>
                      )}
                    </details>
                  </li>
                ))}
              </ol>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
