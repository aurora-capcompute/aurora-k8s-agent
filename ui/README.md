# Aurora UI

A standalone web app that talks to the Aurora agent's HTTP API. It acts as the
**web channel**: switch between the manifests (FunctionInstances) bound to web
Channels, browse each manifest's threads, runs, revisions, and call graphs, and
chat live (with task approvals to come).

It lives under `ui/` in the agent repo for now but is a self-contained Vite app
with its own build and container; it can be split into its own repo later.

## Develop

```sh
cd ui
npm install
# point at a running agent API (default http://localhost:8081)
AURORA_API_TARGET=http://localhost:8081 npm run dev
```

Vite proxies `/api` (including the SSE stream) to the agent.

Run the agent locally headless, fs control plane, API on :8081:

```sh
AURORA_SOURCES=none AURORA_CONTROL_PLANE=fs AURORA_RESOURCES_DIR=./resources \
AURORA_API_ADDR=:8081 ./aurora-k8s-agent
```

## Build & container

```sh
npm run build          # outputs dist/
docker build -t aurora-ui .
docker run -p 8080:8080 -e AURORA_API_UPSTREAM=<agent-api-host:port> aurora-ui
```

nginx serves the SPA and proxies `/api` to `AURORA_API_UPSTREAM` (the agent's API
Service, e.g. `<release>-api.<namespace>.svc:80`).

## API consumed

- `GET /api/manifests` — manifests bound to the web channel (the switcher)
- `GET /api/manifests/{name}/threads` — that manifest's threads
- `POST /api/manifests/{name}/threads` — create a thread under a manifest
- `GET /api/threads/{id}/graph` — runs → revisions → journals
- `GET /api/runs/{id}/graph` — delegation/call tree
- `POST /api/threads/{id}/messages` — chat
- `POST /api/runs/{id}/{stop,retry}`, `POST /api/tasks/{id}/resolve`
- `GET /api/threads/{id}/events` — live Server-Sent Events
