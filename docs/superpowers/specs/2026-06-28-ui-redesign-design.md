# UI Redesign — Aurora K8s Agent

**Date:** 2026-06-28  
**Status:** Approved

---

## Goal

Replace the current fixed three-column layout with a two-pane shell (sidebar + main) plus an on-demand debug drawer. The result should feel minimal and precise: clean typography, no overflow, every element purposeful.

---

## Layout Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Sidebar (280px)  │  Main (flex-1)          │ Drawer (420px)│
│                   │                         │  (hidden by   │
│  [manifest ▾]     │  Thread title   [Debug] │   default,    │
│  + New thread     │─────────────────────────│  slides in)   │
│                   │  transcript             │               │
│  • Thread A  ●    │                         │  run_id       │
│  • Thread B       │  …                      │  [Stop][Rsme] │
│  • Thread C       │                         │               │
│                   │─────────────────────────│  Tasks        │
│                   │  [composer textarea]    │  Call graph   │
│                   │                  [Send] │  Journal      │
└─────────────────────────────────────────────────────────────┘
```

The drawer is not a separate column — it overlays the right portion of the main pane (position: absolute / fixed within the main area) and is toggled by clicking "Debug" or a run's meta link in the transcript. This keeps the chat readable even when the drawer is open.

---

## Visual System

| Token | Value |
|---|---|
| `--accent` | `#1565c0` |
| `--accent-light` | `color-mix(in srgb, var(--accent) 12%, transparent)` |
| `--bg` | `#fafafa` |
| `--surface` | `#ffffff` |
| `--border` | `#e8e8e8` |
| `--text` | `#1a1a1a` |
| `--text-secondary` | `#6b7280` |
| `--text-tertiary` | `#9ca3af` |
| `--red` | `#c62828` |
| `--green` | `#2e7d32` |
| `--orange` | `#ef6c00` |

**Type scale:** body `14px / 1.5`, meta `12px`, labels `11px uppercase letter-spacing 0.06em`, code `12px ui-monospace`.

**Spacing unit:** `8px`. Padding/margin values are multiples of 4px.

**Status dots:** 8px circle, colour matches status badge colour. Used in the sidebar thread list to signal an active run without text.

**No box-shadows on list rows.** Active state = `background: var(--accent-light)` + `2px left border var(--accent)`. Hover state = `background: var(--bg)`.

**Text overflow:** every truncated string gets `overflow: hidden; text-overflow: ellipsis; white-space: nowrap` with a bounded width — no unbounded flex children.

---

## Components

### `App.tsx`

Holds top-level state: selected manifest, selected thread, drawer state (open + which run ID). Renders `<Sidebar>` + `<MainPane>`. No layout logic elsewhere.

### `Sidebar.tsx` *(new — replaces manifests pane + threads pane)*

**Manifest selector (top):** a single `<button>` showing the current manifest name + a `▾` caret. Clicking opens a small dropdown list of manifest names. Selecting one closes the dropdown and loads that manifest's threads. If only one manifest exists the button is still rendered but non-interactive (no caret, muted colour).

**New-thread button:** text link style (`+ New thread`, accent colour), sits directly below the manifest selector. Disabled while a creation is in-flight.

**Thread list:** unstyled, fills remaining sidebar height with `overflow-y: auto`. Each thread item:
- One `<button>` element, full width, `text-align: left`
- Line 1: title (bold, 14px, truncated with ellipsis)
- Line 2: last-message preview (12px, `--text-secondary`, truncated) — derived from `ThreadSummary` fields already returned by the API (the `title` field currently holds "New thread"; the preview comes from the first run's message if available, else empty)
- Right side: relative timestamp (12px, `--text-tertiary`)
- Status dot (8px circle, positioned top-right of the item): visible only when `active_run_id` is set; colour = `--accent` for running, `--orange` for yielded/waiting
- Active item: `--accent-light` background + left border
- Hover: `--bg` background

Thread list is **not** re-fetched on an interval. The parent already subscribes to SSE for the open thread; a separate `useEffect` in the sidebar component polls `manifestThreads` every 10s so new threads created via Telegram appear without a page reload. When the open thread changes, the thread list is also reloaded.

### `ThreadView.tsx` *(redesigned — no tabs)*

**Header bar:** `height: 48px`, border-bottom. Left: thread title (16px, semibold, truncated). Right: `<button className="debug-toggle">Debug</button>` — outline style when drawer is closed, filled (accent) when open. Clicking toggles the drawer with no run pre-selected (the drawer shows a placeholder until a run is clicked).

**Transcript:** `flex: 1; overflow-y: auto; padding: 24px 32px`. Max content width `680px`, centred with `margin: 0 auto`. Each exchange:

```
[user bubble — right-aligned, accent bg, white text, border-radius 18px 18px 4px 18px]
[assistant text — left-aligned, no bubble, --text colour, line-height 1.6]
[meta row — 11px, --text-tertiary: "run_abc · completed · r1" as a clickable link]
[progress block — only while running: monospace lines, --bg bg, border-left 3px accent]
```

The meta row's run ID is a `<button class="link">` that opens the debug drawer for that run.

**Composer:** `height: 72px`, border-top, `padding: 12px 16px`. Textarea fills width minus send button. Send button: accent background, `border-radius: 8px`, `padding: 0 20px`. Shift+Enter for newline, Enter to send.

**Empty state (no thread selected):** centred text "Select a thread or create one" with a muted `+` icon above it.

### `DebugDrawer.tsx` *(new component)*

Positioned `absolute right: 0; top: 0; bottom: 0; width: 420px` within `MainPane` (which has `position: relative`). Transition: `transform: translateX(100%)` → `translateX(0)`, `transition: transform 200ms ease`. Overlay — does not shrink the transcript.

**Header (48px):** matches thread view header height. Left: `run_id` in monospace (12px) + status badge. Right: close `×` button.

**Controls row:** Stop / Retry (resume) / Retry (restart) as small outline buttons. Stop is muted (gray border) when the run is terminal. Retry buttons are disabled for queued/running runs.

**Pending tasks section:** shown only when tasks exist in `state: "pending"`. Section label "Pending approvals". Each task: orange left-border card (existing `.task` style, kept), with Approve / Deny buttons.

**Call graph:** `height: 280px`. ReactFlow canvas (existing `<CallGraph>` component, unchanged). Shown only when `entries.length > 0`.

**Journal:** section label "Journal · N steps". Each entry: a single `<div>` row (not `<details>`):
- Status badge (small, inline)
- `<code>` call name
- Revision tag `r1` (purple, monospace)
- Outcome message if present (truncated, `--text-secondary`)
- Clicking the row expands an inline block below showing args JSON + result JSON (dark code block). Only one row expanded at a time.

The `RunPanel.tsx` file is deleted; its logic (data fetching, controls, task list, journal) moves into `DebugDrawer.tsx`.

### `styles.css`

Full rewrite. Keeps the same CSS custom properties approach but with the updated token set above. All existing class names that are still used are kept so `CallGraph.tsx`, `Login.tsx`, and badge classes continue to work without changes.

---

## Data / API

No API changes. The drawer fetches `api.journal(runID)` and `api.tasks(runID)` on mount and when `tick` changes (same pattern as current `RunPanel`). SSE subscription stays in `ThreadView`; the drawer receives `tick` as a prop.

`ThreadSummary` already has `active_run_id` and `updated_at` — both are used by the sidebar (status dot and relative timestamp).

---

## Files Changed

| File | Change |
|---|---|
| `ui/src/App.tsx` | Restructure layout; add drawer state |
| `ui/src/styles.css` | Full rewrite |
| `ui/src/components/Sidebar.tsx` | New file |
| `ui/src/components/ThreadView.tsx` | Redesign; remove tabs |
| `ui/src/components/DebugDrawer.tsx` | New file (absorbs RunPanel) |
| `ui/src/components/RunPanel.tsx` | Deleted |
| `ui/src/components/CallGraph.tsx` | Unchanged |
| `ui/src/components/Login.tsx` | Unchanged |
| `ui/src/components/graph.ts` | Unchanged |
| `ui/src/api.ts` | Unchanged |
| `ui/src/types.ts` | Unchanged |

---

## Out of Scope

- Thread title editing
- Thread deletion
- Dark mode
- Mobile layout
- Markdown rendering in messages
