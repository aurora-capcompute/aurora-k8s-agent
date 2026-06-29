import { useCallback, useEffect, useRef, useState } from "react";
import { api, UnauthorizedError } from "./api";
import type { ManifestInfo, ThreadSummary } from "./types";
import { HotkeyHelp } from "./components/HotkeyHelp";
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
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loginKey, setLoginKey] = useState(0);
  const [showHelp, setShowHelp] = useState(false);

  // Stable refs so the hotkey handler never needs to re-register.
  const threadsRef = useRef(threads);
  threadsRef.current = threads;
  const threadRef = useRef(thread);
  threadRef.current = thread;
  const newThreadRef = useRef<() => void>(() => {});
  const handleThreadSelectRef = useRef<(id: string) => void>(() => {});

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

  // Keep refs current so the hotkey handler (registered once) sees fresh values.
  newThreadRef.current = () => void newThread();
  handleThreadSelectRef.current = handleThreadSelect;

  // Escape always blurs any focused input, restoring hotkey availability.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      const t = document.activeElement;
      if (t instanceof HTMLElement && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) {
        t.blur();
      }
    };
    window.addEventListener("keydown", handler, { capture: true });
    return () => window.removeEventListener("keydown", handler, { capture: true });
  }, []);

  // Global hotkeys: n, J/K, `, ?
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;

      if (e.key === "?") { setShowHelp((h) => !h); return; }
      if (e.key === "n") { newThreadRef.current(); return; }
      if (e.key === "`") { setSidebarCollapsed((c) => !c); return; }

      if (e.key === "J" || e.key === "K") {
        const ts = threadsRef.current;
        if (ts.length === 0) return;
        const sorted = [...ts].sort(
          (a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
        );
        const idx = sorted.findIndex((t) => t.id === threadRef.current);
        const next = e.key === "J" ? idx + 1 : idx - 1;
        const chosen = sorted[Math.max(0, Math.min(sorted.length - 1, next))];
        if (chosen) handleThreadSelectRef.current(chosen.id);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []); // stable: all values accessed through refs or stable setters

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
      {showHelp && <HotkeyHelp onClose={() => setShowHelp(false)} />}
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
        collapsed={sidebarCollapsed}
        onToggle={() => setSidebarCollapsed((c) => !c)}
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
            onRunSelect={(id) => setDrawerRunID(id)}
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
