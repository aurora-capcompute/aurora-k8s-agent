import React, { useCallback, useEffect, useRef, useState } from "react";
import { api, UnauthorizedError } from "../api";
import type { Decision, JournalEntry, RunGraphNode, RunStatus, TaskSnapshot } from "../types";
import { CallGraph } from "./CallGraph";

function renderJournalSection(
  label: string,
  runID: string,
  entries: JournalEntry[],
  expanded: string | null,
  setExpanded: (key: string | null) => void,
  refs: React.MutableRefObject<Map<string, HTMLElement>>,
  subtitle?: string,
) {
  const toggle = (key: string) => setExpanded(expanded === key ? null : key);
  return (
    <div className="drawer-section" key={runID}>
      <div className="section-label">
        {label} · {entries.length} steps
        {subtitle && <span className="child-msg"> — {subtitle.slice(0, 60)}</span>}
      </div>
      <div className="journal-list">
        {entries.length === 0 && (
          <div className="drawer-empty-sm">No steps recorded yet.</div>
        )}
        {entries.map((entry) => {
          const key = `${runID}:${entry.index}-${entry.revision}`;
          return (
            <div
              key={key}
              ref={(el) => {
                if (el) refs.current.set(key, el);
                else refs.current.delete(key);
              }}
            >
              <button
                className={`journal-row${expanded === key ? " expanded" : ""}`}
                onClick={() => toggle(key)}
              >
                <span className={`badge badge-${entry.outcome.status}`}>
                  {entry.outcome.status}
                </span>
                <code className="journal-name">{entry.call.name}</code>
                <span className="rev-tag">r{entry.revision}</span>
                {entry.outcome.message && (
                  <span className="journal-msg">{entry.outcome.message}</span>
                )}
              </button>
              {expanded === key && (
                <div className="journal-detail">
                  {entry.call.args !== undefined && (
                    <pre className="json">{JSON.stringify(entry.call.args, null, 2)}</pre>
                  )}
                  {entry.outcome.result !== undefined && (
                    <pre className="json result">{JSON.stringify(entry.outcome.result, null, 2)}</pre>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

type ChildData = {
  runID: string;
  status: RunStatus;
  entries: JournalEntry[];
};

function flattenChildren(node: RunGraphNode): { runID: string; status: RunStatus }[] {
  const result: { runID: string; status: RunStatus }[] = [];
  for (const child of node.children ?? []) {
    result.push({ runID: child.run_id, status: child.status });
    result.push(...flattenChildren(child));
  }
  return result;
}

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
  const [childData, setChildData] = useState<ChildData[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const journalRowRefs = useRef<Map<string, HTMLElement>>(new Map());

  useEffect(() => {
    if (expanded) {
      journalRowRefs.current.get(expanded)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }
  }, [expanded]);

  const handleError = useCallback(
    (e: unknown) => {
      if (e instanceof UnauthorizedError) onUnauthorized?.();
      else setError(String(e));
    },
    [onUnauthorized],
  );

  const loadChildren = useCallback(async (rootRunID: string) => {
    try {
      const graph = await api.runGraph(rootRunID);
      const children = flattenChildren(graph);
      const loaded = await Promise.all(
        children.map(async ({ runID: cid, status }) => {
          const entries = await api.journal(cid).catch(() => [] as JournalEntry[]);
          return { runID: cid, status, entries };
        }),
      );
      return loaded;
    } catch {
      return [];
    }
  }, []);

  const reload = useCallback(async () => {
    if (!runID) return;
    try {
      const [j, t] = await Promise.all([api.journal(runID), api.tasks(runID)]);
      setJournal(j);
      setTasks(t);
      setError(null);
    } catch (e) {
      handleError(e);
    }
    const loaded = await loadChildren(runID);
    setChildData(loaded);
  }, [runID, handleError, loadChildren]);

  useEffect(() => {
    setJournal([]);
    setTasks([]);
    setChildData([]);
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
      const loaded = await loadChildren(runID);
      if (!stale) setChildData(loaded);
    })();
    return () => { stale = true; };
  }, [runID, handleError, loadChildren]);

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

  const handleNodeClick = useCallback((index: number, revision: number) => {
    setExpanded(`${runID ?? ""}:${index}-${revision}`);
  }, [runID]);

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
          <div className="drawer-left">
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

            {renderJournalSection("Journal", runID ?? "", journal, expanded, setExpanded, journalRowRefs)}

            {childData.map((child) =>
              renderJournalSection(
                `↳ ${child.runID.slice(0, 12)} · ${child.status}`,
                child.runID,
                child.entries,
                expanded,
                setExpanded,
                journalRowRefs,
              ),
            )}
          </div>

          <div className="drawer-right">
            <CallGraph entries={journal} onNodeClick={handleNodeClick} />
          </div>
        </div>
      )}
    </div>
  );
}
