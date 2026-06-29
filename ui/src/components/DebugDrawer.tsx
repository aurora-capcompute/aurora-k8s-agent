import React, { useCallback, useEffect, useRef, useState } from "react";
import { api, UnauthorizedError } from "../api";
import type { Decision, JournalEntry, RunGraphNode, RunStatus, TaskSnapshot } from "../types";

function isTyping(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  return !!(t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable));
}

// ── helpers ───────────────────────────────────────────────────────

function summarize(entry: JournalEntry): string {
  const src =
    entry.outcome.message ??
    (entry.outcome.result !== undefined ? JSON.stringify(entry.outcome.result) : "");
  return src.length > 80 ? src.slice(0, 79) + "…" : src;
}

// Extract a human-readable display name. Falls back to stripping brain URLs
// (e.g. "wasm://path/to/research_agent.wasm" → "research_agent").
function displayName(name: string | undefined, runID: string): string {
  if (name && name.trim()) {
    const base = name.includes("/") ? name.split("/").pop()! : name;
    return base.replace(/\.(wasm|js|mjs)$/, "");
  }
  return runID.slice(0, 8);
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

// Compute the revision a child section should show, matching the parent's viewRevision.
function childEffectiveRev(revMap: Record<string, JournalEntry[]>, viewRevision: number): number {
  const revs = Object.keys(revMap).map(Number).sort((a, b) => a - b);
  if (revs.includes(viewRevision)) return viewRevision;
  return revs.filter((r) => r <= viewRevision).slice(-1)[0] ?? revs[0] ?? 1;
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
  focusedKey,
  rowRefs,
}: {
  runID: string;
  entries: JournalEntry[];
  viewRevision?: number;
  expanded: string | null;
  setExpanded: (k: string | null) => void;
  focusedKey?: string | null;
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
          const isFocused = focusedKey === key;
          const isNew = viewRevision === undefined || entry.revision === viewRevision;
          const rowClass = [
            "log-row",
            !isNew ? "log-row-carried" : "",
            isOpen ? "log-row-open" : "",
            isFocused ? "log-row-focused" : "",
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
  open,
  onToggle,
  expanded,
  setExpanded,
  focusedKey,
  rowRefs,
}: {
  child: ChildData;
  viewRevision: number;
  open: boolean;
  onToggle: () => void;
  expanded: string | null;
  setExpanded: (k: string | null) => void;
  focusedKey?: string | null;
  rowRefs: React.MutableRefObject<Map<string, HTMLElement>>;
}) {
  const hdrKey = `child-hdr:${child.runID}`;
  const isFocused = focusedKey === hdrKey;
  const effectiveRev = childEffectiveRev(child.revMap, viewRevision);
  const entries = child.revMap[String(effectiveRev)] ?? [];
  const label = displayName(child.name, child.runID);

  return (
    <div className="child-section">
      <button
        ref={(el) => {
          if (el) rowRefs.current.set(hdrKey, el);
          else rowRefs.current.delete(hdrKey);
        }}
        className={`child-header${isFocused ? " child-header-focused" : ""}`}
        onClick={onToggle}
      >
        <span className="child-caret">{open ? "▾" : "▸"}</span>
        <code className="child-agent-name">{label}</code>
        <code className="child-id">{child.runID.slice(0, 12)}</code>
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
            focusedKey={focusedKey}
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
  const [childOpenSet, setChildOpenSet] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<string | null>(null);
  const [focusedKey, setFocusedKey] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [viewRevision, setViewRevision] = useState<number>(1);
  const rowRefs = useRef<Map<string, HTMLElement>>(new Map());

  // Stable refs so the hotkey handler (registered once) always reads fresh values.
  const focusedKeyRef = useRef<string | null>(null);
  focusedKeyRef.current = focusedKey;
  const navItemsRef = useRef<string[]>([]);
  const pendingRef = useRef<TaskSnapshot[]>([]);
  const runIDRef = useRef(runID);
  runIDRef.current = runID;
  const childOpenSetRef = useRef(childOpenSet);
  childOpenSetRef.current = childOpenSet;
  const actRef = useRef<(fn: () => Promise<unknown>) => Promise<void>>(async () => {});

  const toggleChildOpen = (childRunID: string) => {
    setChildOpenSet((prev) => {
      const next = new Set(prev);
      if (next.has(childRunID)) next.delete(childRunID);
      else next.add(childRunID);
      return next;
    });
  };

  // Jump to latest revision whenever revMap changes.
  useEffect(() => {
    const revs = Object.keys(revMap).map(Number);
    setViewRevision(revs.length === 0 ? 1 : Math.max(...revs));
  }, [revMap]);

  // Scroll expanded/focused rows into view.
  useEffect(() => {
    if (expanded) rowRefs.current.get(expanded)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }, [expanded]);

  useEffect(() => {
    if (focusedKey) rowRefs.current.get(focusedKey)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }, [focusedKey]);

  // Keyboard shortcuts — registered once; all mutable values accessed through refs.
  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") { onClose(); return; }
      if (isTyping(e)) return;

      const ptasks = pendingRef.current;
      const rid = runIDRef.current;

      switch (e.key) {
        case "[":
        case "]": {
          setViewRevision((cur) => {
            const revs = Object.keys(revMap).map(Number).sort((a, b) => a - b);
            if (revs.length <= 1) return cur;
            const idx = revs.indexOf(cur);
            const next = e.key === "[" ? idx - 1 : idx + 1;
            return revs[Math.max(0, Math.min(revs.length - 1, next))] ?? cur;
          });
          setExpanded(null);
          setFocusedKey(null);
          break;
        }
        case "j":
        case "k": {
          const items = navItemsRef.current;
          if (items.length === 0) break;
          const cur = focusedKeyRef.current;
          const idx = cur ? items.indexOf(cur) : -1;
          const next = e.key === "j" ? idx + 1 : idx - 1;
          const newKey = items[Math.max(0, Math.min(items.length - 1, next))] ?? null;
          focusedKeyRef.current = newKey;
          setFocusedKey(newKey);
          break;
        }
        case "Enter": {
          const fk = focusedKeyRef.current;
          if (!fk) break;
          if (fk.startsWith("child-hdr:")) {
            toggleChildOpen(fk.slice("child-hdr:".length));
          } else {
            setExpanded((prev) => (prev === fk ? null : fk));
          }
          break;
        }
        case "r": if (rid) void actRef.current(() => api.retryRun(rid, "resume")); break;
        case "R": if (rid) void actRef.current(() => api.retryRun(rid, "restart")); break;
        case "s": if (rid) void actRef.current(() => api.stopRun(rid)); break;
        case "y": {
          const first = ptasks[0];
          if (first) void actRef.current(() => api.resolveTask(first.id, first.webhook_token, { decision: "approved", actor: "ui" }));
          break;
        }
        case "x": {
          const first = ptasks[0];
          if (first) void actRef.current(() => api.resolveTask(first.id, first.webhook_token, { decision: "denied", actor: "ui" }));
          break;
        }
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [open, onClose, revMap]); // revMap needed for [/] revision navigation

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
    setChildOpenSet(new Set());
    setExpanded(null);
    setFocusedKey(null);
    setError(null);
    if (!runID) return;
    let stale = false;
    void (async () => {
      try {
        const [rm, t] = await Promise.all([api.journalRevisions(runID), api.tasks(runID)]);
        if (!stale) { setRevMap(rm); setTasks(t); setError(null); }
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
  useEffect(() => { void reloadRef.current(); }, [tick]);

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
  actRef.current = act;

  const resolve = (task: TaskSnapshot, decision: Decision) =>
    act(() => api.resolveTask(task.id, task.webhook_token, { decision, actor: "ui" }));

  const pending = tasks.filter((t) => t.state === "pending");
  const revisions = Object.keys(revMap).map(Number).sort((a, b) => a - b);
  const viewedEntries = revMap[String(viewRevision)] ?? [];

  // Build the unified keyboard navigation list: parent entries → child headers → child entries.
  const navItems: string[] = [];
  if (runID) {
    for (const e of viewedEntries) navItems.push(`${runID}:${e.index}-${e.revision}`);
  }
  for (const c of childData) {
    const hdrKey = `child-hdr:${c.runID}`;
    navItems.push(hdrKey);
    if (childOpenSet.has(c.runID)) {
      const effRev = childEffectiveRev(c.revMap, viewRevision);
      for (const e of c.revMap[String(effRev)] ?? []) {
        navItems.push(`${c.runID}:${e.index}-${e.revision}`);
      }
    }
  }

  // Keep refs current for the stable hotkey handler.
  navItemsRef.current = navItems;
  pendingRef.current = pending;

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
                      <button className="approve" disabled={busy} onClick={() => void resolve(t, "approved")}>
                        Approve
                      </button>
                      <button className="deny" disabled={busy} onClick={() => void resolve(t, "denied")}>
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
              {revisions.length > 1 && <span className="label-sub"> (r{viewRevision})</span>}
            </div>
            <JournalTable
              runID={runID}
              entries={viewedEntries}
              viewRevision={viewRevision}
              expanded={expanded}
              setExpanded={setExpanded}
              focusedKey={focusedKey}
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
                  open={childOpenSet.has(child.runID)}
                  onToggle={() => toggleChildOpen(child.runID)}
                  expanded={expanded}
                  setExpanded={setExpanded}
                  focusedKey={focusedKey}
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
