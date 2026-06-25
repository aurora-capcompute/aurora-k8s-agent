import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import type { Decision, JournalEntry, TaskSnapshot } from "../types";

// RunPanel shows a single run's journal, pending tasks (HITL approvals), and
// run controls (stop / retry). It is driven by a runID selected in the graph.
export function RunPanel({
  runID,
  onChanged,
}: {
  runID: string;
  onChanged?: () => void;
}) {
  const [journal, setJournal] = useState<JournalEntry[]>([]);
  const [tasks, setTasks] = useState<TaskSnapshot[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const reload = useCallback(async () => {
    try {
      const [j, t] = await Promise.all([api.journal(runID), api.tasks(runID)]);
      setJournal(j);
      setTasks(t);
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }, [runID]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      await reload();
      onChanged?.();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const resolve = (task: TaskSnapshot, decision: Decision) =>
    act(() =>
      api.resolveTask(task.id, task.webhook_token, { decision, actor: "ui" }),
    );

  const pending = tasks.filter((t) => t.state === "pending");

  return (
    <div className="run-panel">
      <div className="run-panel-head">
        <strong>{runID}</strong>
        <div className="run-panel-controls">
          <button disabled={busy} onClick={() => void act(() => api.stopRun(runID))}>
            Stop
          </button>
          <button
            disabled={busy}
            onClick={() => void act(() => api.retryRun(runID, "resume"))}
            title="Fork from the failure point and resume"
          >
            Retry (resume)
          </button>
          <button
            disabled={busy}
            onClick={() => void act(() => api.retryRun(runID, "restart"))}
            title="Restart the run from the beginning"
          >
            Retry (restart)
          </button>
        </div>
      </div>

      {error && <div className="error">{error}</div>}

      {pending.length > 0 && (
        <div className="tasks">
          <div className="section-label">Pending approvals</div>
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
      )}

      <div className="section-label">Journal · {journal.length} steps</div>
      <ol className="journal">
        {journal.map((entry) => (
          <li key={entry.index} className={`outcome-${entry.outcome.status}`}>
            <details>
              <summary>
                <span className={`badge badge-${entry.outcome.status}`}>
                  {entry.outcome.status}
                </span>
                <code>{entry.call.name}</code>
                {entry.outcome.message ? ` — ${entry.outcome.message}` : ""}
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
  );
}
