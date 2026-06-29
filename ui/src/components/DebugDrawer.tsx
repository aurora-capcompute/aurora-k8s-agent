import React, { useCallback, useEffect, useRef, useState } from "react";
import { api, UnauthorizedError } from "../api";
import type { Decision, JournalEntry, RunGraphNode, RunStatus, TaskSnapshot } from "../types";

// ── helpers ───────────────────────────────────────────────────────

function summarize(entry: JournalEntry): string {
  const src =
    entry.outcome.message ??
    (entry.outcome.result !== undefined ? JSON.stringify(entry.outcome.result) : "");
  return src.length > 80 ? src.slice(0, 79) + "…" : src;
}

type ChildData = {
  runID: string;
  name?: string;
  status: RunStatus;
  revMap: Record<string, JournalEntry[]>;
};

function flattenChildren(node: RunGraphNode): { runID: string; name?: string; status: RunStatus }[] {
  const out: { runID: string; name?: string; status: RunStatus }[] = [];
  for (const c of node.children ?? []) {
    out.push({ runID: c.run_id, name: c.name, status: c.status });
    out.push(...flattenChildren(c));
  }
  return out;
}

// ── RevisionSlider ────────────────────────────────────────────────

function RevisionSlider({
  revisions,
  value,
  onChange,
}: {
  revisions: number[];
  value: number;
  onChange: (r: number) => void;
}) {
  if (revisions.length <= 1) return null;
  const curIdx = revisions.indexOf(value);
  return (
    <div className="rev-slider">
      <div className="rev-slider-head">
        <span className="section-label" style={{ margin: 0 }}>History</span>
        <span className="rev-slider-pos">viewing r{value}</span>
      </div>
      <div className="rev-track">
        {revisions.map((r, i) => (
          <button
            key={r}
            className={`rev-tick${i === curIdx ? " current" : ""}${i < curIdx ? " past" : ""}`}
            onClick={() => onChange(r)}
            title={`Revision ${r}`}
          >
            <span className="tick-dot" />
            <span className="tick-label">r{r}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

// ── JournalTable ──────────────────────────────────────────────────

function JournalTable({
  runID,
  entries,
  viewRevision,
  expanded,
  setExpanded,
  rowRefs,
}: {
  runID: string;
  entries: JournalEntry[];
  // viewRevision: the revision being shown; entries whose .revision < viewRevision
  // were carried forward from an earlier attempt (dimmed slightly).
  viewRevision?: number;
  expanded: string | null;
  setExpanded: (k: string | null) => void;
  rowRefs: React.MutableRefObject<Map<string, HTMLElement>>;
}) {
  if (entries.length === 0) {
    return <div className="log-empty">No steps recorded yet.</div>;
  }
  return (
    <table className="log-table">
      <thead>
        <tr>
          <th className="col-idx">#</th>
          <th className="col-rev">Rev</th>
          <th className="col-status">Status</th>
          <th className="col-call">Tool Call</th>
          <th className="col-summary">Summary</th>
        </tr>
      </thead>
      <tbody>
        {entries.map((entry) => {
          const key = `${runID}:${entry.index}-${entry.revision}`;
          const isOpen = expanded === key;
          // "new" = written during the viewed revision; "carried" = shared prefix from earlier
          const isNew = viewRevision === undefined || entry.revision === viewRevision;
          const rowClass = [
            "log-row",
            !isNew ? "log-row-carried" : "",
            isOpen ? "log-row-open" : "",
          ]
            .filter(Boolean)
            .join(" ");

          return (
            <React.Fragment key={key}>
              <tr
                ref={(el) => {
                  if (el) rowRefs.current.set(key, el);
                  else rowRefs.current.delete(key);
                }}
                className={rowClass}
                onClick={() => setExpanded(isOpen ? null : key)}
              >
                <td className="col-idx">{entry.index}</td>
                <td className="col-rev">
                  <span className={`jrev${!isNew ? "" : entry.revision > 1 ? " jrev-retry" : ""}`}>
                    r{entry.revision}
                  </span>
                </td>
                <td className="col-status">
                  <span className={`badge badge-${entry.outcome.status}`}>
                    {entry.outcome.status}
                  </span>
                </td>
                <td className="col-call">
                  <code className="call-name">{entry.call.name}</code>
                </td>
                <td className="col-summary">{isNew ? summarize(entry) : ""}</td>
              </tr>
              {isOpen && (
                <tr className="log-detail-row">
                  <td colSpan={5}>
                    <div className="log-detail">
                      {entry.call.args !== undefined && (
                        <pre className="json">{JSON.stringify(entry.call.args, null, 2)}</pre>
                      )}
                      {entry.outcome.result !== undefined && (
                        <pre className="json result">
                          {JSON.stringify(entry.outcome.result, null, 2)}
                        </pre>
                      )}
                    </div>
                  </td>
                </tr>
              )}
            </React.Fragment>
          );
        })}
      </tbody>
    </table>
  );
}

// ── ChildSection ──────────────────────────────────────────────────

function ChildSection({
  child,
  viewRevision,
  expanded,
  setExpanded,
  rowRefs,
}: {
  child: ChildData;
  viewRevision: number;
  expanded: string | null;
  setExpanded: (k: string | null) => void;
  rowRefs: React.MutableRefObject<Map<string, HTMLElement>>;
}) {
  const [open, setOpen] = useState(false);
  // Show entries at the parent's viewed revision. Fall back to nearest available
  // revision if the child doesn't have an exact match (e.g. child has fewer retries).
  const childRevs = Object.keys(child.revMap).map(Number).sort((a, b) => a - b);
  const effectiveRev =
    childRevs.includes(viewRevision)
      ? viewRevision
      : (childRevs.filter((r) => r <= viewRevision).slice(-1)[0] ?? childRevs[0] ?? 1);
  const entries = child.revMap[String(effectiveRev)] ?? [];
  return (
    <div className="child-section">
      <button className="child-header" onClick={() => setOpen((v) => !v)}>
        <span className="child-caret">{open ? "▾" : "▸"}</span>
        {child.name && <code className="child-agent-name">{child.name}</code>}
        <code className="child-id">{child.runID.slice(0, 16)}</code>
        <span className={`badge badge-${child.status}`}>{child.status}</span>
        <span className="child-count">{entries.length} steps</span>
      </button>
      {open && (
        <div className="child-table-wrap">
          <JournalTable
            runID={child.runID}
            entries={entries}
            viewRevision={effectiveRev}
            expanded={expanded}
            setExpanded={setExpanded}
            rowRefs={rowRefs}
          />
        </div>
      )}
    </div>
  );
}

// ── DebugDrawer ───────────────────────────────────────────────────

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
  const [revMap, setRevMap] = useState<Record<string, JournalEntry[]>>({});
  const [tasks, setTasks] = useState<TaskSnapshot[]>([]);
  const [childData, setChildData] = useState<ChildData[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [viewRevision, setViewRevision] = useState<number>(1);
  const rowRefs = useRef<Map<string, HTMLElement>>(new Map());

  // Jump to latest revision whenever revMap changes
  useEffect(() => {
    const revs = Object.keys(revMap).map(Number);
    setViewRevision(revs.length === 0 ? 1 : Math.max(...revs));
  }, [revMap]);

  useEffect(() => {
    if (expanded) {
      rowRefs.current.get(expanded)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }
  }, [expanded]);

  const handleError = useCallback(
    (e: unknown) => {
      if (e instanceof UnauthorizedError) onUnauthorized?.();
      else setError(String(e));
    },
    [onUnauthorized],
  );

  const loadChildren = useCallback(async (rid: string) => {
    try {
      const graph = await api.runGraph(rid);
      const children = flattenChildren(graph);
      return await Promise.all(
        children.map(async ({ runID: cid, name: agentName, status }) => {
          const revMap = await api
            .journalRevisions(cid)
            .catch(() => ({}) as Record<string, JournalEntry[]>);
          return { runID: cid, name: agentName, status, revMap };
        }),
      );
    } catch {
      return [];
    }
  }, []);

  const reload = useCallback(async () => {
    if (!runID) return;
    try {
      const [rm, t] = await Promise.all([api.journalRevisions(runID), api.tasks(runID)]);
      setRevMap(rm);
      setTasks(t);
      setError(null);
    } catch (e) {
      handleError(e);
    }
    const loaded = await loadChildren(runID);
    setChildData(loaded);
  }, [runID, handleError, loadChildren]);

  useEffect(() => {
    setRevMap({});
    setTasks([]);
    setChildData([]);
    setExpanded(null);
    setError(null);
    if (!runID) return;
    let stale = false;
    void (async () => {
      try {
        const [rm, t] = await Promise.all([api.journalRevisions(runID), api.tasks(runID)]);
        if (!stale) {
          setRevMap(rm);
          setTasks(t);
          setError(null);
        }
      } catch (e) {
        if (!stale) handleError(e);
      }
      const loaded = await loadChildren(runID);
      if (!stale) setChildData(loaded);
    })();
    return () => {
      stale = true;
    };
  }, [runID, handleError, loadChildren]);

  const reloadRef = useRef(reload);
  useEffect(() => {
    reloadRef.current = reload;
  }, [reload]);
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
    act(() => api.resolveTask(task.id, task.webhook_token, { decision, actor: "ui" }));

  const pending = tasks.filter((t) => t.state === "pending");
  const revisions = Object.keys(revMap).map(Number).sort((a, b) => a - b);
  // Each revision snapshot is a complete per-revision view from the backend.
  // entry.revision tells us when it was first written; entries with revision < viewRevision
  // are carried forward from an earlier attempt.
  const viewedEntries = revMap[String(viewRevision)] ?? [];

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
        <div className="drawer-scroll">
          <RevisionSlider
            revisions={revisions}
            value={viewRevision}
            onChange={(r) => {
              setViewRevision(r);
              setExpanded(null);
            }}
          />

          {error && (
            <div className="error" style={{ margin: "8px 16px" }}>
              {error}
            </div>
          )}

          <div className="drawer-controls">
            <button disabled={busy} onClick={() => void act(() => api.stopRun(runID))}>
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

          <div className="drawer-section">
            <div className="section-label">
              Journal · {viewedEntries.length} steps
              {revisions.length > 1 && (
                <span className="label-sub"> (r{viewRevision})</span>
              )}
            </div>
            <JournalTable
              runID={runID}
              entries={viewedEntries}
              viewRevision={viewRevision}
              expanded={expanded}
              setExpanded={setExpanded}
              rowRefs={rowRefs}
            />
          </div>

          {childData.length > 0 && (
            <div className="drawer-section">
              <div className="section-label">Subruns · {childData.length}</div>
              {childData.map((child) => (
                <ChildSection
                  key={child.runID}
                  child={child}
                  viewRevision={viewRevision}
                  expanded={expanded}
                  setExpanded={setExpanded}
                  rowRefs={rowRefs}
                />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
