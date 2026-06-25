import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { ManifestInfo, ThreadSummary } from "./types";
import { ThreadView } from "./components/ThreadView";

export default function App() {
  const [manifests, setManifests] = useState<ManifestInfo[]>([]);
  const [manifest, setManifest] = useState<string | null>(null);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [thread, setThread] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .manifests()
      .then((m) => {
        setManifests(m);
        if (m.length > 0) setManifest((cur) => cur ?? m[0].name);
      })
      .catch((e) => setError(String(e)));
  }, []);

  const loadThreads = useCallback((name: string) => {
    api
      .manifestThreads(name)
      .then(setThreads)
      .catch((e) => setError(String(e)));
  }, []);

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
      setError(String(e));
    }
  };

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
          <ThreadView threadID={thread} />
        ) : (
          <div className="empty center">Select or create a thread.</div>
        )}
      </main>
    </div>
  );
}
