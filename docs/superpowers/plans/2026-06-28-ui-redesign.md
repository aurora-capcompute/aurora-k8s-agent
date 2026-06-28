# UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the fixed three-column layout with a two-pane shell (sidebar + main) plus an on-demand slide-in debug drawer.

**Architecture:** `App.tsx` owns layout state (selected manifest/thread, drawer open + run); a new `Sidebar.tsx` replaces both the manifests pane and threads pane; `ThreadView.tsx` becomes tab-free chat-only; a new `DebugDrawer.tsx` absorbs `RunPanel.tsx` and renders as a `position:absolute` overlay sliding in from the right. No API changes.

**Tech Stack:** React 18, TypeScript, Vite, ReactFlow (unchanged), dagre (unchanged), plain CSS custom properties (no CSS-in-JS).

## Global Constraints

- No new npm dependencies.
- All existing class names used by `CallGraph.tsx`, `Login.tsx`, and `graph.ts` must survive (`.badge`, `.badge-*`, `.json`, `.json.result`, `.rev-tag`, `.link`, `.task`, `.task-*`, `.section-label`).
- `CallGraph.tsx`, `Login.tsx`, `graph.ts`, `api.ts`, `types.ts` — do not modify.
- Verification command after every task: `cd ui && npx tsc --noEmit && echo "OK"` from the `aurora-k8s-agent` directory.
- Build command: `cd ui && npm run build`.
- Working directory for all commands: `/home/rob/workspace/cap_aurora/aurora-k8s-agent`.

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `ui/src/styles.css` | Rewrite | Design tokens, all layout and component styles |
| `ui/src/components/Sidebar.tsx` | Create | Manifest selector + thread list |
| `ui/src/components/DebugDrawer.tsx` | Create | Slide-in run inspector (journal, graph, tasks, controls) |
| `ui/src/components/ThreadView.tsx` | Rewrite | Tab-free chat view; renders DebugDrawer |
| `ui/src/App.tsx` | Rewrite | App shell: layout state, sidebar + main pane |
| `ui/src/components/RunPanel.tsx` | Delete | Absorbed by DebugDrawer |

---

## Task 1: CSS Foundation

**Files:**
- Modify: `ui/src/styles.css` (full rewrite)

**Interfaces:**
- Produces: all CSS classes and custom properties used by Tasks 2–5.

- [ ] **Step 1: Replace styles.css**

Overwrite the entire file with:

```css
/* ── Design tokens ────────────────────────────────────────────── */
:root {
  --accent:          #1565c0;
  --accent-light:    color-mix(in srgb, var(--accent) 10%, transparent);
  --bg:              #fafafa;
  --surface:         #ffffff;
  --border:          #e8e8e8;
  --text:            #1a1a1a;
  --text-secondary:  #6b7280;
  --text-tertiary:   #9ca3af;
  --red:             #c62828;
  --green:           #2e7d32;
  --orange:          #ef6c00;

  font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif;
  font-size: 14px;
  line-height: 1.5;
  color: var(--text);
}

* { box-sizing: border-box; }
body { margin: 0; }

/* ── App shell ────────────────────────────────────────────────── */
.app-shell {
  display: flex;
  height: 100vh;
  overflow: hidden;
}

/* ── Sidebar ──────────────────────────────────────────────────── */
.sidebar {
  width: 280px;
  flex-shrink: 0;
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  background: var(--surface);
  overflow: hidden;
}

.sidebar-top {
  padding: 16px 12px 8px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}

.manifest-label {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-tertiary);
  padding: 0 4px;
  margin-bottom: 8px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.manifest-dropdown {
  position: relative;
  margin-bottom: 8px;
}

.manifest-trigger {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border: 1px solid var(--border);
  background: var(--surface);
  border-radius: 8px;
  padding: 7px 10px;
  cursor: pointer;
  font: inherit;
}

.manifest-trigger:hover {
  background: var(--bg);
}

.manifest-name {
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
  text-align: left;
}

.manifest-caret {
  font-size: 10px;
  color: var(--text-tertiary);
  flex-shrink: 0;
  margin-left: 6px;
}

.manifest-menu {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.08);
  z-index: 100;
  overflow: hidden;
}

.manifest-option {
  display: block;
  width: 100%;
  text-align: left;
  padding: 8px 12px;
  font: inherit;
  font-size: 13px;
  background: none;
  border: none;
  cursor: pointer;
  color: var(--text);
}

.manifest-option:hover { background: var(--bg); }

.manifest-option.active {
  color: var(--accent);
  font-weight: 600;
}

.new-thread-btn {
  display: block;
  width: 100%;
  text-align: left;
  padding: 6px 6px;
  font: inherit;
  font-size: 13px;
  color: var(--accent);
  background: none;
  border: none;
  cursor: pointer;
  border-radius: 6px;
}

.new-thread-btn:hover { background: var(--accent-light); }

.thread-list {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
}

.sidebar-empty {
  padding: 16px 8px;
  font-size: 13px;
  color: var(--text-tertiary);
}

.thread-item {
  display: block;
  width: 100%;
  text-align: left;
  background: none;
  border: none;
  border-left: 2px solid transparent;
  border-radius: 0 6px 6px 0;
  padding: 8px 10px;
  cursor: pointer;
  font: inherit;
  margin-bottom: 1px;
}

.thread-item:hover { background: var(--bg); }

.thread-item.active {
  background: var(--accent-light);
  border-left-color: var(--accent);
}

.thread-item-main {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.thread-item-title {
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.status-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--accent);
  flex-shrink: 0;
  animation: dot-pulse 2s ease-in-out infinite;
}

@keyframes dot-pulse {
  0%, 100% { opacity: 1; }
  50%       { opacity: 0.35; }
}

.thread-item-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 2px;
}

.thread-item-count,
.thread-item-time {
  font-size: 11px;
  color: var(--text-tertiary);
}

/* ── Main pane ────────────────────────────────────────────────── */
.main-pane {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  position: relative;
  overflow: hidden;
}

.empty-state {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
}

.empty-state-text {
  font-size: 14px;
  color: var(--text-tertiary);
}

/* ── Thread shell ─────────────────────────────────────────────── */
.thread-shell {
  display: flex;
  flex-direction: column;
  height: 100%;
  position: relative;
  overflow: hidden;
}

.thread-header {
  height: 48px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
  background: var(--surface);
}

.thread-header-title {
  font-size: 14px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
  margin-right: 12px;
}

.debug-toggle {
  flex-shrink: 0;
  border: 1px solid var(--border);
  background: var(--surface);
  border-radius: 6px;
  padding: 5px 12px;
  font: inherit;
  font-size: 12px;
  color: var(--text-secondary);
  cursor: pointer;
}

.debug-toggle:hover { background: var(--bg); }

.debug-toggle.active {
  background: var(--accent);
  border-color: var(--accent);
  color: #fff;
}

.thread-body {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

/* ── Transcript ───────────────────────────────────────────────── */
.transcript {
  flex: 1;
  overflow-y: auto;
}

.transcript-inner {
  max-width: 680px;
  margin: 0 auto;
  padding: 24px 16px;
}

.transcript-empty {
  color: var(--text-tertiary);
  font-size: 13px;
  text-align: center;
  margin-top: 48px;
}

.exchange { margin-bottom: 24px; }

.msg {
  padding: 10px 14px;
  margin-bottom: 4px;
  white-space: pre-wrap;
  word-break: break-word;
}

.msg.user {
  background: var(--accent);
  color: #fff;
  border-radius: 18px 18px 4px 18px;
  margin-left: auto;
  max-width: 72%;
  display: table;
}

.msg.assistant {
  color: var(--text);
  line-height: 1.65;
}

.msg.error-msg {
  color: var(--red);
  font-size: 13px;
}

.progress-block {
  background: var(--bg);
  border-left: 3px solid var(--accent);
  border-radius: 0 6px 6px 0;
  padding: 8px 12px;
  margin-bottom: 4px;
}

.progress-header {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--accent);
  margin-bottom: 4px;
}

.progress-line {
  font-family: ui-monospace, "Cascadia Mono", monospace;
  font-size: 12px;
  color: var(--text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.5;
}

.run-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  font-size: 11px;
}

/* ── Composer ─────────────────────────────────────────────────── */
.composer-wrap {
  border-top: 1px solid var(--border);
  background: var(--surface);
  flex-shrink: 0;
}

.composer {
  max-width: 680px;
  margin: 0 auto;
  padding: 12px 16px;
  display: flex;
  gap: 8px;
}

.composer textarea {
  flex: 1;
  min-width: 0;
  resize: none;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 10px 12px;
  font: inherit;
  font-size: 14px;
  line-height: 1.5;
  outline: none;
  height: 48px;
}

.composer textarea:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-light);
}

.composer button {
  flex-shrink: 0;
  border: none;
  background: var(--accent);
  color: #fff;
  border-radius: 10px;
  padding: 0 20px;
  font: inherit;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
}

.composer button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* ── Debug drawer ─────────────────────────────────────────────── */
.debug-drawer {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: 420px;
  background: var(--surface);
  border-left: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  transform: translateX(100%);
  transition: transform 200ms ease;
  z-index: 10;
}

.debug-drawer.open { transform: translateX(0); }

.drawer-header {
  height: 48px;
  display: flex;
  align-items: center;
  padding: 0 12px 0 16px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
  gap: 8px;
}

.drawer-run-id {
  font-family: ui-monospace, "Cascadia Mono", monospace;
  font-size: 12px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.drawer-close {
  flex-shrink: 0;
  border: none;
  background: none;
  font-size: 14px;
  color: var(--text-tertiary);
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 4px;
  line-height: 1;
}

.drawer-close:hover {
  background: var(--bg);
  color: var(--text);
}

.drawer-empty {
  padding: 32px 16px;
  font-size: 13px;
  color: var(--text-tertiary);
  text-align: center;
}

.drawer-body {
  flex: 1;
  overflow-y: auto;
}

.drawer-controls {
  display: flex;
  gap: 6px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
}

.drawer-controls button {
  border: 1px solid var(--border);
  background: var(--surface);
  border-radius: 6px;
  padding: 5px 10px;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  color: var(--text-secondary);
}

.drawer-controls button:hover:not(:disabled) {
  border-color: var(--text-secondary);
  color: var(--text);
}

.drawer-controls button:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.drawer-section {
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
}

.drawer-section:last-child { border-bottom: none; }

.drawer-graph {
  height: 260px;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  margin-top: 4px;
}

/* ── Journal list ─────────────────────────────────────────────── */
.journal-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-top: 4px;
}

.journal-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  text-align: left;
  background: none;
  border: none;
  border-radius: 6px;
  padding: 5px 6px;
  cursor: pointer;
  font: inherit;
  min-width: 0;
  overflow: hidden;
}

.journal-row:hover,
.journal-row.expanded { background: var(--bg); }

.journal-name {
  font-family: ui-monospace, "Cascadia Mono", monospace;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.journal-msg {
  font-size: 11px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100px;
  flex-shrink: 0;
}

.journal-detail { margin: 2px 0 4px; }

.drawer-empty-sm {
  font-size: 12px;
  color: var(--text-tertiary);
  padding: 8px 0;
}

/* ── Shared ───────────────────────────────────────────────────── */
.section-label {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-tertiary);
  margin-bottom: 8px;
}

.error {
  background: #fdecea;
  color: var(--red);
  padding: 8px 12px;
  font-size: 13px;
  border-radius: 6px;
  margin: 8px 16px;
}

/* ── Badges (keep for CallGraph + DebugDrawer) ────────────────── */
.badge {
  display: inline-block;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  padding: 1px 6px;
  border-radius: 10px;
  color: #fff;
  background: #455a64;
  vertical-align: middle;
  flex-shrink: 0;
  white-space: nowrap;
}

.badge-completed,
.badge-result    { background: var(--green); }
.badge-failed    { background: var(--red); }
.badge-running   { background: var(--accent); }
.badge-queued    { background: #6a1b9a; }
.badge-yielded,
.badge-waiting_task,
.badge-stopping  { background: var(--orange); }
.badge-stopped,
.badge-interrupted { background: #455a64; }

/* ── Rev tag (keep for ThreadView + DebugDrawer) ──────────────── */
.rev-tag {
  font-size: 10px;
  color: #6a1b9a;
  font-family: ui-monospace, "Cascadia Mono", monospace;
  flex-shrink: 0;
  white-space: nowrap;
}

/* ── Link button ──────────────────────────────────────────────── */
.link {
  border: none;
  background: none;
  padding: 0;
  color: var(--accent);
  font: inherit;
  font-size: 11px;
  font-family: ui-monospace, "Cascadia Mono", monospace;
  cursor: pointer;
}

.link:hover { text-decoration: underline; }

/* ── HITL tasks ───────────────────────────────────────────────── */
.tasks .task {
  border: 1px solid var(--border);
  border-left: 3px solid var(--orange);
  border-radius: 8px;
  background: var(--surface);
  padding: 8px 10px;
  margin-bottom: 8px;
}

.task-summary {
  font-weight: 600;
  font-size: 13px;
  margin-bottom: 4px;
}

.task-call {
  font-size: 11px;
  color: var(--text-secondary);
  font-family: ui-monospace, "Cascadia Mono", monospace;
}

.task-actions {
  display: flex;
  gap: 6px;
  margin-top: 8px;
}

.task-actions button {
  border: none;
  border-radius: 6px;
  padding: 4px 12px;
  color: #fff;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}

.task-actions .approve { background: var(--green); }
.task-actions .deny    { background: var(--red); }

/* ── JSON dumps (keep for DebugDrawer journal) ────────────────── */
.json {
  background: #1e1e1e;
  color: #d4d4d4;
  border-radius: 6px;
  padding: 8px;
  margin: 4px 0;
  font-size: 12px;
  overflow-x: auto;
  white-space: pre;
  font-family: ui-monospace, "Cascadia Mono", monospace;
}

.json.result { border-left: 3px solid var(--green); }

/* ── Login ────────────────────────────────────────────────────── */
.login-loading {
  color: var(--text-tertiary);
  font-size: 14px;
}

.login-wrap {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100vh;
  background: var(--bg);
}

.login {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 32px;
  width: 320px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.login h1 {
  font-size: 18px;
  font-weight: 600;
  margin: 0 0 4px;
}

.login input {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 9px 11px;
  font: inherit;
  outline: none;
}

.login input:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-light);
}

.login button[type="submit"] {
  width: 100%;
  border: none;
  background: var(--accent);
  color: #fff;
  border-radius: 8px;
  padding: 10px;
  font: inherit;
  font-weight: 600;
  cursor: pointer;
  margin-top: 4px;
}

.login button[type="submit"]:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

- [ ] **Step 2: Verify type-check passes**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent/ui && npx tsc --noEmit && echo "OK"
```

Expected: `OK` (CSS changes don't affect TS).

- [ ] **Step 3: Commit**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent
git add ui/src/styles.css
git commit -m "style: rewrite CSS — two-pane shell with sidebar, drawer, and minimal design tokens"
```

---

## Task 2: Sidebar Component

**Files:**
- Create: `ui/src/components/Sidebar.tsx`

**Interfaces:**
- Consumes: `ManifestInfo`, `ThreadSummary` from `../types`
- Produces:
  ```ts
  export function Sidebar(props: {
    manifests: ManifestInfo[];
    selectedManifest: string | null;
    onManifestChange: (name: string) => void;
    threads: ThreadSummary[];
    selectedThread: string | null;
    onThreadSelect: (id: string) => void;
    onNewThread: () => void;
    onReloadThreads: () => void;
  }): JSX.Element
  ```

- [ ] **Step 1: Create `ui/src/components/Sidebar.tsx`**

```tsx
import { useEffect, useRef, useState } from "react";
import type { ManifestInfo, ThreadSummary } from "../types";

function relativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

export function Sidebar({
  manifests,
  selectedManifest,
  onManifestChange,
  threads,
  selectedThread,
  onThreadSelect,
  onNewThread,
  onReloadThreads,
}: {
  manifests: ManifestInfo[];
  selectedManifest: string | null;
  onManifestChange: (name: string) => void;
  threads: ThreadSummary[];
  selectedThread: string | null;
  onThreadSelect: (id: string) => void;
  onNewThread: () => void;
  onReloadThreads: () => void;
}) {
  const [dropdownOpen, setDropdownOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (
        dropdownRef.current &&
        !dropdownRef.current.contains(e.target as Node)
      ) {
        setDropdownOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, []);

  // Poll for new threads (e.g. from Telegram) every 10 s.
  useEffect(() => {
    const id = setInterval(onReloadThreads, 10_000);
    return () => clearInterval(id);
  }, [onReloadThreads]);

  const current = manifests.find((m) => m.name === selectedManifest);

  return (
    <aside className="sidebar">
      <div className="sidebar-top">
        {manifests.length <= 1 ? (
          <div className="manifest-label">
            {current?.name ?? "No manifest"}
          </div>
        ) : (
          <div className="manifest-dropdown" ref={dropdownRef}>
            <button
              className="manifest-trigger"
              onClick={() => setDropdownOpen((o) => !o)}
            >
              <span className="manifest-name">
                {current?.name ?? "Select…"}
              </span>
              <span className="manifest-caret">▾</span>
            </button>
            {dropdownOpen && (
              <div className="manifest-menu">
                {manifests.map((m) => (
                  <button
                    key={m.name}
                    className={`manifest-option${
                      m.name === selectedManifest ? " active" : ""
                    }`}
                    onClick={() => {
                      onManifestChange(m.name);
                      setDropdownOpen(false);
                    }}
                  >
                    {m.name}
                  </button>
                ))}
              </div>
            )}
          </div>
        )}
        <button className="new-thread-btn" onClick={onNewThread}>
          + New thread
        </button>
      </div>

      <div className="thread-list">
        {threads.length === 0 && (
          <div className="sidebar-empty">No threads yet.</div>
        )}
        {threads.map((t) => (
          <button
            key={t.id}
            className={`thread-item${t.id === selectedThread ? " active" : ""}`}
            onClick={() => onThreadSelect(t.id)}
          >
            <div className="thread-item-main">
              <span className="thread-item-title">
                {t.title && t.title !== "New thread"
                  ? t.title
                  : t.id.slice(0, 16)}
              </span>
              {t.active_run_id && <span className="status-dot" />}
            </div>
            <div className="thread-item-meta">
              <span className="thread-item-count">
                {t.run_count} {t.run_count === 1 ? "run" : "runs"}
              </span>
              <span className="thread-item-time">
                {relativeTime(t.updated_at)}
              </span>
            </div>
          </button>
        ))}
      </div>
    </aside>
  );
}
```

- [ ] **Step 2: Verify type-check**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent/ui && npx tsc --noEmit && echo "OK"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent
git add ui/src/components/Sidebar.tsx
git commit -m "feat: add Sidebar component — manifest selector + thread list with live status dots"
```

---

## Task 3: DebugDrawer Component

**Files:**
- Create: `ui/src/components/DebugDrawer.tsx`

**Interfaces:**
- Consumes: `api.journal`, `api.tasks`, `api.stopRun`, `api.retryRun`, `api.resolveTask` from `../api`; `JournalEntry`, `TaskSnapshot`, `Decision` from `../types`; `CallGraph` from `./CallGraph`
- Produces:
  ```ts
  export function DebugDrawer(props: {
    runID: string | null;
    open: boolean;
    tick?: number;
    onClose: () => void;
    onChanged?: () => void;
    onUnauthorized?: () => void;
  }): JSX.Element
  ```

- [ ] **Step 1: Create `ui/src/components/DebugDrawer.tsx`**

```tsx
import { useCallback, useEffect, useState } from "react";
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
  const [expanded, setExpanded] = useState<number | null>(null);
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
    void reload();
  }, [reload]);

  useEffect(() => {
    void reload();
  }, [reload, tick]);

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

  const toggleExpand = (index: number) =>
    setExpanded((cur) => (cur === index ? null : index));

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
                      expanded === entry.index ? " expanded" : ""
                    }`}
                    onClick={() => toggleExpand(entry.index)}
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
                  {expanded === entry.index && (
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
```

- [ ] **Step 2: Verify type-check**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent/ui && npx tsc --noEmit && echo "OK"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent
git add ui/src/components/DebugDrawer.tsx
git commit -m "feat: add DebugDrawer — slide-in run inspector with journal, call graph, tasks, and controls"
```

---

## Task 4: ThreadView Redesign

**Files:**
- Modify: `ui/src/components/ThreadView.tsx` (full rewrite)

**Interfaces:**
- Consumes: `DebugDrawer` from `./DebugDrawer`; `api`, `subscribe`, `UnauthorizedError` from `../api`; `RunStatus`, `ThreadGraph` from `../types`
- Produces:
  ```ts
  export function ThreadView(props: {
    threadID: string;
    drawerOpen: boolean;
    drawerRunID: string | null;
    onToggleDrawer: () => void;
    onRunClick: (runID: string) => void;
    onDrawerClose: () => void;
    onUnauthorized?: () => void;
    onReloadThreads?: () => void;
  }): JSX.Element
  ```

- [ ] **Step 1: Overwrite `ui/src/components/ThreadView.tsx`**

```tsx
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
  const activeDrawerRunID =
    drawerRunID ?? (graph?.runs?.at(-1)?.run_id ?? null);

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
```

- [ ] **Step 2: Verify type-check**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent/ui && npx tsc --noEmit && echo "OK"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent
git add ui/src/components/ThreadView.tsx
git commit -m "feat: redesign ThreadView — tab-free chat with debug drawer toggle and run meta links"
```

---

## Task 5: App Shell + Cleanup

**Files:**
- Modify: `ui/src/App.tsx` (full rewrite)
- Delete: `ui/src/components/RunPanel.tsx`

**Interfaces:**
- Consumes: `Sidebar` from `./components/Sidebar`; `ThreadView` from `./components/ThreadView`; `Login` from `./components/Login`; `api`, `UnauthorizedError` from `./api`; `ManifestInfo`, `ThreadSummary` from `./types`
- Produces: root `<App>` component (no change to external interface)

- [ ] **Step 1: Overwrite `ui/src/App.tsx`**

```tsx
import { useCallback, useEffect, useState } from "react";
import { api, UnauthorizedError } from "./api";
import type { ManifestInfo, ThreadSummary } from "./types";
import { Login } from "./components/Login";
import { Sidebar } from "./components/Sidebar";
import { ThreadView } from "./components/ThreadView";

export default function App() {
  const [manifests, setManifests] = useState<ManifestInfo[]>([]);
  const [manifest, setManifest] = useState<string | null>(null);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [thread, setThread] = useState<string | null>(null);
  const [drawerRunID, setDrawerRunID] = useState<string | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loginKey, setLoginKey] = useState(0);

  const onUnauthorized = useCallback(() => setNeedsLogin(true), []);

  useEffect(() => {
    setLoading(true);
    api
      .manifests()
      .then((m) => {
        const ms = m ?? [];
        setNeedsLogin(false);
        setManifests(ms);
        if (ms.length > 0) setManifest((cur) => cur ?? ms[0].name);
      })
      .catch((e) => {
        if (e instanceof UnauthorizedError) setNeedsLogin(true);
        else setError(String(e));
      })
      .finally(() => setLoading(false));
  }, [loginKey]);

  const loadThreads = useCallback(
    (name: string) => {
      api
        .manifestThreads(name)
        .then((t) => setThreads(t ?? []))
        .catch((e) => {
          if (e instanceof UnauthorizedError) onUnauthorized();
          else setError(String(e));
        });
    },
    [onUnauthorized],
  );

  useEffect(() => {
    if (manifest) {
      setThread(null);
      setDrawerOpen(false);
      setDrawerRunID(null);
      loadThreads(manifest);
    }
  }, [manifest, loadThreads]);

  const newThread = async () => {
    if (!manifest) return;
    try {
      const created = await api.createThread(manifest);
      loadThreads(manifest);
      setThread(created.id);
      setDrawerOpen(false);
      setDrawerRunID(null);
    } catch (e) {
      if (e instanceof UnauthorizedError) onUnauthorized();
      else setError(String(e));
    }
  };

  const handleManifestChange = (name: string) => {
    setManifest(name);
    setDrawerOpen(false);
    setDrawerRunID(null);
  };

  const handleThreadSelect = (id: string) => {
    setThread(id);
    setDrawerOpen(false);
    setDrawerRunID(null);
  };

  const openDrawer = (runID: string) => {
    setDrawerRunID(runID);
    setDrawerOpen(true);
  };

  const toggleDrawer = () => {
    setDrawerOpen((o) => !o);
    // Don't clear drawerRunID — keeps the last-viewed run selected.
  };

  if (loading) {
    return (
      <div className="login-wrap">
        <div className="login-loading">Loading…</div>
      </div>
    );
  }

  if (needsLogin) {
    return (
      <Login
        onLogin={() => {
          setNeedsLogin(false);
          setLoginKey((k) => k + 1);
        }}
      />
    );
  }

  return (
    <div className="app-shell">
      <Sidebar
        manifests={manifests}
        selectedManifest={manifest}
        onManifestChange={handleManifestChange}
        threads={threads}
        selectedThread={thread}
        onThreadSelect={handleThreadSelect}
        onNewThread={() => void newThread()}
        onReloadThreads={() => {
          if (manifest) loadThreads(manifest);
        }}
      />
      <div className="main-pane">
        {error && <div className="error">{error}</div>}
        {thread ? (
          <ThreadView
            threadID={thread}
            drawerOpen={drawerOpen}
            drawerRunID={drawerRunID}
            onToggleDrawer={toggleDrawer}
            onRunClick={openDrawer}
            onDrawerClose={() => setDrawerOpen(false)}
            onUnauthorized={onUnauthorized}
            onReloadThreads={() => {
              if (manifest) loadThreads(manifest);
            }}
          />
        ) : (
          <div className="empty-state">
            <div className="empty-state-text">
              Select a thread or create one.
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Delete `RunPanel.tsx`**

```bash
rm /home/rob/workspace/cap_aurora/aurora-k8s-agent/ui/src/components/RunPanel.tsx
```

- [ ] **Step 3: Verify type-check and build**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent/ui && npx tsc --noEmit && echo "TS OK" && npm run build && echo "BUILD OK"
```

Expected output ends with `BUILD OK`. No TypeScript errors.

- [ ] **Step 4: Commit**

```bash
cd /home/rob/workspace/cap_aurora/aurora-k8s-agent
git add ui/src/App.tsx
git rm ui/src/components/RunPanel.tsx
git commit -m "feat: wire App shell — sidebar+main layout, drawer state, delete RunPanel"
```

---

## Self-Review

**Spec coverage:**

| Spec requirement | Task |
|---|---|
| Sidebar (280px): manifest dropdown, new-thread, thread list with status dot + relative time | Task 2 |
| Thread title and Debug toggle in header | Task 4 |
| Tab-free chat transcript, user/assistant bubbles, progress block | Task 4 |
| Run meta row with run ID link opening drawer | Task 4 |
| DebugDrawer: position:absolute, slide-in, 420px, 200ms transition | Task 1 (CSS) + Task 3 |
| Drawer: controls (stop/resume/restart), pending tasks, call graph, journal | Task 3 |
| Journal rows: expandable inline, single-expand, args+result JSON | Task 3 |
| Full CSS rewrite: tokens, no box-shadows on rows, ellipsis on all truncated strings | Task 1 |
| Delete RunPanel.tsx | Task 5 |
| No API changes | ✓ (no api.ts changes in any task) |
| CallGraph/Login/graph.ts unchanged | ✓ |

**Placeholder scan:** None found — all steps contain complete code.

**Type consistency:**
- `DebugDrawer` props: `runID: string | null`, `open: boolean`, `tick?: number`, `onClose`, `onChanged?`, `onUnauthorized?` — used identically in Task 3 (definition) and Task 4 (consumption). ✓
- `ThreadView` props: defined in Task 4, consumed in Task 5 (App.tsx) with matching names and types. ✓
- `Sidebar` props: defined in Task 2, consumed in Task 5 with matching names and types. ✓
- `relativeTime(iso: string): string` — defined and used only in Task 2. ✓
