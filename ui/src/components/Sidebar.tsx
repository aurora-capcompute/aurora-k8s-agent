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
  collapsed,
  onToggle,
}: {
  manifests: ManifestInfo[];
  selectedManifest: string | null;
  onManifestChange: (name: string) => void;
  threads: ThreadSummary[];
  selectedThread: string | null;
  onThreadSelect: (id: string) => void;
  onNewThread: () => void;
  onReloadThreads: () => void;
  collapsed?: boolean;
  onToggle?: () => void;
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
  const sorted = [...threads].sort(
    (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
  );

  return (
    <aside className={`sidebar${collapsed ? " collapsed" : ""}`}>
      <button className="sidebar-toggle" onClick={onToggle} title={collapsed ? "Expand sidebar" : "Collapse sidebar"}>
        {collapsed ? "»" : "«"}
      </button>
      <div className="sidebar-main">
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
        {sorted.length === 0 && (
          <div className="sidebar-empty">No threads yet.</div>
        )}
        {sorted.map((t) => (
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
      </div>
    </aside>
  );
}
