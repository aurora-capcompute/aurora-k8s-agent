import { useCallback, useEffect, useState } from "react";
import { api, UnauthorizedError } from "./api";
import type { ManifestInfo, ThreadSummary } from "./types";
import { Login } from "./components/Login";
import { ThreadView } from "./components/ThreadView";

export default function App() {
  const [manifests, setManifests] = useState<ManifestInfo[]>([]);
  const [manifest, setManifest] = useState<string | null>(null);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [thread, setThread] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [loading, setLoading] = useState(true);
  // Bumped after a successful login to re-trigger manifest loading.
  const [loginKey, setLoginKey] = useState(0);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerRunID, setDrawerRunID] = useState<string | null>(null);

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
        if (e instanceof UnauthorizedError) {
          setNeedsLogin(true);
        } else {
          setError(String(e));
        }
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
      loadThreads(manifest);
    }
  }, [manifest, loadThreads]);

  const newThread = async () => {
    if (!manifest) return;
    try {
      const created = await api.createThread(manifest);
      loadThreads(manifest);
      setThread(created.id);
    } catch (e) {
      if (e instanceof UnauthorizedError) onUnauthorized();
      else setError(String(e));
    }
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
    <div className="app">
      <aside className="pane manifests">
        <h2>Manifests</h2>
        {manifests.length === 0 && <div className="empty">None bound.</div>}
        {manifests.map((m) => (
          <button
            key={m.name}
            className={m.name === manifest ? "row active" : "row"}
            onClick={() => setManifest(m.name)}
          >
            <div className="row-title">{m.name}</div>
            <div className="row-sub">{m.brain}</div>
          </button>
        ))}
      </aside>

      <section className="pane threads">
        <div className="threads-head">
          <h2>Threads</h2>
          <button disabled={!manifest} onClick={() => void newThread()}>
            + New
          </button>
        </div>
        {threads.length === 0 && <div className="empty">No threads.</div>}
        {threads.map((t) => (
          <button
            key={t.id}
            className={t.id === thread ? "row active" : "row"}
            onClick={() => setThread(t.id)}
          >
            <div className="row-title">{t.title || t.id}</div>
            <div className="row-sub">
              {t.run_count} run{t.run_count === 1 ? "" : "s"}
            </div>
          </button>
        ))}
      </section>

      <main className="pane main">
        {error && <div className="error">{error}</div>}
        {thread ? (
          <ThreadView
            threadID={thread}
            drawerOpen={drawerOpen}
            drawerRunID={drawerRunID}
            onToggleDrawer={() => setDrawerOpen((o) => !o)}
            onRunClick={(runID) => {
              setDrawerRunID(runID);
              setDrawerOpen(true);
            }}
            onDrawerClose={() => setDrawerOpen(false)}
            onUnauthorized={onUnauthorized}
            onReloadThreads={() => {
              if (manifest) loadThreads(manifest);
            }}
          />
        ) : (
          <div className="empty center">Select or create a thread.</div>
        )}
      </main>
    </div>
  );
}
