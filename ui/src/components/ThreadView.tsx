import { useCallback, useEffect, useRef, useState } from "react";
import { api, subscribe } from "../api";
import type { RunGraphNode, RunStatus, ThreadGraph } from "../types";
import { CallGraph } from "./CallGraph";
import { RunPanel } from "./RunPanel";

type Tab = "chat" | "graph" | "revisions";

function StatusBadge({ status }: { status: RunStatus }) {
  return <span className={`badge badge-${status}`}>{status}</span>;
}

export function ThreadView({ threadID }: { threadID: string }) {
  const [graph, setGraph] = useState<ThreadGraph | null>(null);
  const [runGraph, setRunGraph] = useState<RunGraphNode | null>(null);
  const [selectedRun, setSelectedRun] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("chat");
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);

  const reload = useCallback(async () => {
    try {
      const g = await api.threadGraph(threadID);
      setGraph(g);
      const last = g.runs[g.runs.length - 1];
      if (last) setRunGraph(await api.runGraph(last.run_id));
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }, [threadID]);

  useEffect(() => {
    setGraph(null);
    setRunGraph(null);
    setSelectedRun(null);
    void reload();
    const unsubscribe = subscribe(threadID, () => void reload());
    return unsubscribe;
  }, [threadID, reload]);

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
    } catch (e) {
      setError(String(e));
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
            {graph?.runs.map((run) => (
              <div key={run.run_id} className="exchange">
                <div className="msg user">{run.message}</div>
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
        (runGraph ? (
          <div className="graph-split">
            <div className="graph-canvas">
              <CallGraph
                root={runGraph}
                selected={selectedRun}
                onSelect={setSelectedRun}
              />
            </div>
            {selectedRun && (
              <div className="graph-side">
                <RunPanel runID={selectedRun} onChanged={() => void reload()} />
              </div>
            )}
          </div>
        ) : (
          <div className="empty">No runs yet.</div>
        ))}

      {tab === "revisions" && (
        <div className="revisions">
          {graph?.runs.map((run) => (
            <div key={run.run_id} className="rev-run">
              <div className="rev-run-head">
                <button className="link" onClick={() => inspect(run.run_id)}>
                  {run.run_id}
                </button>{" "}
                <StatusBadge status={run.status} /> — {run.message}
              </div>
              {run.revisions.map((rev) => (
                <details key={rev.revision} className="rev">
                  <summary>
                    revision {rev.revision}
                    {rev.forked
                      ? ` · forked from ${rev.fork_parent} @ ${rev.fork_offset}`
                      : " · base"}{" "}
                    · {rev.entries.length} steps
                  </summary>
                  <ol className="journal">
                    {rev.entries.map((entry) => (
                      <li
                        key={entry.index}
                        className={`outcome-${entry.outcome.status}`}
                      >
                        <details>
                          <summary>
                            <span
                              className={`badge badge-${entry.outcome.status}`}
                            >
                              {entry.outcome.status}
                            </span>
                            <code>{entry.call.name}</code>
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
                </details>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
