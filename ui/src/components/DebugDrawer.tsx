import { useCallback, useEffect, useRef, useState } from "react";
import { api, UnauthorizedError } from "../api";
import type { Decision, JournalEntry, TaskSnapshot } from "../types";
import { CallGraph } from "./CallGraph";

export function DebugDrawer({
  runID,
  open,
  tick,
  onClose,
  onChanged,
  onUnauthorized,
}: {
  runID: string | null;
  open: boolean;
  tick?: number;
  onClose: () => void;
  onChanged?: () => void;
  onUnauthorized?: () => void;
}) {
  const [journal, setJournal] = useState<JournalEntry[]>([]);
  const [tasks, setTasks] = useState<TaskSnapshot[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const handleError = useCallback(
    (e: unknown) => {
      if (e instanceof UnauthorizedError) onUnauthorized?.();
      else setError(String(e));
    },
    [onUnauthorized],
  );

  const reload = useCallback(async () => {
    if (!runID) return;
    try {
      const [j, t] = await Promise.all([
        api.journal(runID),
        api.tasks(runID),
      ]);
      setJournal(j);
      setTasks(t);
      setError(null);
    } catch (e) {
      handleError(e);
    }
  }, [runID, handleError]);

  useEffect(() => {
    setJournal([]);
    setTasks([]);
    setExpanded(null);
    setError(null);
    if (!runID) return;
    let stale = false;
    void (async () => {
      try {
        const [j, t] = await Promise.all([api.journal(runID), api.tasks(runID)]);
        if (!stale) { setJournal(j); setTasks(t); setError(null); }
      } catch (e) {
        if (!stale) handleError(e);
      }
    })();
    return () => { stale = true; };
  }, [runID, handleError]);

  const reloadRef = useRef(reload);
  useEffect(() => { reloadRef.current = reload; }, [reload]);

  useEffect(() => {
    void reloadRef.current();
  }, [tick]);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      await reload();
      onChanged?.();
    } catch (e) {
      handleError(e);
    } finally {
      setBusy(false);
    }
  };

  const resolve = (task: TaskSnapshot, decision: Decision) =>
    act(() =>
      api.resolveTask(task.id, task.webhook_token, { decision, actor: "ui" }),
    );

  const pending = tasks.filter((t) => t.state === "pending");

  const toggleExpand = (index: number, revision: number) => {
    const key = `${index}-${revision}`;
    setExpanded((cur) => (cur === key ? null : key));
  };

  return (
    <div className={`debug-drawer${open ? " open" : ""}`}>
      <div className="drawer-header">
        <span className="drawer-run-id">{runID ?? "—"}</span>
        <button className="drawer-close" onClick={onClose} aria-label="Close">
          ✕
        </button>
      </div>

      {!runID ? (
        <div className="drawer-empty">Select a run to inspect.</div>
      ) : (
        <div className="drawer-body">
          {error && <div className="error">{error}</div>}

          <div className="drawer-controls">
            <button
              disabled={busy}
              onClick={() => void act(() => api.stopRun(runID))}
            >
              Stop
            </button>
            <button
              disabled={busy}
              onClick={() => void act(() => api.retryRun(runID, "resume"))}
              title="Fork from the failure point and resume"
            >
              Resume
            </button>
            <button
              disabled={busy}
              onClick={() => void act(() => api.retryRun(runID, "restart"))}
              title="Restart the run from the beginning"
            >
              Restart
            </button>
          </div>

          {pending.length > 0 && (
            <div className="drawer-section">
              <div className="section-label">Pending approvals</div>
              <div className="tasks">
                {pending.map((t) => (
                  <div key={t.id} className="task">
                    <div className="task-summary">{t.summary || t.call.name}</div>
                    <code className="task-call">{t.call.name}</code>
                    <div className="task-actions">
                      <button
                        className="approve"
                        disabled={busy}
                        onClick={() => void resolve(t, "approved")}
                      >
                        Approve
                      </button>
                      <button
                        className="deny"
                        disabled={busy}
                        onClick={() => void resolve(t, "denied")}
                      >
                        Deny
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {journal.length > 0 && (
            <div className="drawer-section">
              <div className="section-label">Call graph</div>
              <div className="drawer-graph">
                <CallGraph entries={journal} />
              </div>
            </div>
          )}

          <div className="drawer-section">
            <div className="section-label">
              Journal · {journal.length} steps
            </div>
            <div className="journal-list">
              {journal.length === 0 && (
                <div className="drawer-empty-sm">No steps recorded yet.</div>
              )}
              {journal.map((entry) => (
                <div key={`${entry.index}-${entry.revision}`}>
                  <button
                    className={`journal-row${
                      expanded === `${entry.index}-${entry.revision}` ? " expanded" : ""
                    }`}
                    onClick={() => toggleExpand(entry.index, entry.revision)}
                  >
                    <span
                      className={`badge badge-${entry.outcome.status}`}
                    >
                      {entry.outcome.status}
                    </span>
                    <code className="journal-name">{entry.call.name}</code>
                    <span className="rev-tag">r{entry.revision}</span>
                    {entry.outcome.message && (
                      <span className="journal-msg">
                        {entry.outcome.message}
                      </span>
                    )}
                  </button>
                  {expanded === `${entry.index}-${entry.revision}` && (
                    <div className="journal-detail">
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
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
