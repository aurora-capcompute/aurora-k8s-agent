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
