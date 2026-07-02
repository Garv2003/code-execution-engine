# Code Execution Engine

A LeetCode-style code execution backend in Go — ephemeral Docker containers, worker pool concurrency, and real-time output streaming over SSE.

![Go](https://img.shields.io/badge/Go-1.21-00ADD8?style=flat-square&logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)
![Status](https://img.shields.io/badge/Status-Active-brightgreen?style=flat-square)

---

## Architecture

```
                         ┌─────────────────────────────────────────┐
                         │              API Server                  │
  Client                 │                                          │
  ──────  POST /submit ──►  Request Handler                         │
                         │       │                                  │
                         │       ▼                                  │
                         │  ┌─────────────┐    buffered channel     │
                         │  │ Worker Pool  │◄──────────────────┐    │
                         │  │ (goroutines) │                   │    │
                         │  └──────┬──────┘             Job Queue   │
                         │         │                                │
                         │         ▼                                │
                         │  ┌─────────────────┐                    │
                         │  │ Docker Container │  (ephemeral,       │
                         │  │  - resource caps │   per submission)  │
                         │  │  - timeout ctx   │                    │
                         │  └────────┬─────────┘                   │
                         │           │ stdout/stderr                │
                         │           ▼                              │
                         │  ┌──────────────────┐                   │
                         │  │  Result Publisher │──► Redis Pub/Sub  │
                         │  └──────────────────┘         │         │
                         │                                │         │
                         └────────────────────────────────┼─────────┘
                                                          │
                         ┌────────────────────────────────▼─────────┐
                         │         SSE Stream Handler               │
  Client                 │  GET /result/:id (text/event-stream)     │
  ──────  SSE subscribe ─►  Redis subscriber → chunked response     │
                         └──────────────────────────────────────────┘

  Multi-instance coordination:
  Instance A worker publishes result → Redis channel → Instance B SSE handler delivers to client
```

---

## Features

- **Worker pool with backpressure** — fixed goroutine pool with a buffered channel job queue; submissions are rejected fast when the pool is saturated rather than spawning unbounded goroutines
- **Ephemeral Docker isolation** — each submission runs in a fresh container with strict CPU, memory, and PID limits; containers are destroyed after execution
- **Context-based timeouts** — execution timeout enforced via `context.WithTimeout`; containers are killed cleanly on deadline exceeded
- **Real-time SSE streaming** — output is streamed token-by-token to the client over Server-Sent Events as the container produces it; no polling
- **Redis Pub/Sub for distributed delivery** — results are published to Redis channels so any API server instance can deliver the SSE stream, enabling horizontal scaling
- **Configurable resource limits** — max workers, Docker timeout, memory cap, and CPU quota all tunable via environment variables
- **Multi-file submissions** — send auxiliary source/header files alongside the main entrypoint; all files are written into the container's working directory before execution
- **Job history dashboard** — optional Postgres-backed persistence powers a `/playground/dashboard.html` UI and `GET /dashboard/jobs` endpoint for browsing past executions, status, timing, and output
- **API key authentication** — optional `API_KEYS` gate on all routes except `/health` and the static playground, checked via header or query param (so it also works with `EventSource`, which can't set headers)

---

## Performance

| Metric | Before | After |
|---|---|---|
| Cold-start latency (first output byte) | ~3,200 ms | ~450 ms |
| Concurrent submissions handled | ~50 | 500+ |
| Client-server bandwidth (vs polling) | baseline | ~70% reduction |

Cold-start improvement achieved by pre-pulling base images and reusing Docker network namespaces where isolation allows. SSE bandwidth reduction vs. a 1s-interval polling baseline.

---

## Quick Start

**Requirements:** Go 1.21+, Docker daemon running, Redis instance

```bash
git clone https://github.com/garv2003/code-execution-engine.git
cd code-execution-engine

# Set required env vars (or copy .env.example)
export MAX_WORKERS=20
export DOCKER_TIMEOUT_MS=5000
export REDIS_URL=redis://localhost:6379
export PORT=8080

go run ./cmd/server
```

Development scripts:

```bash
./scripts/start-playground.sh  # API + worker, then opens /playground/
./scripts/run-backend.sh       # API only
./scripts/run-worker.sh        # worker only
./scripts/run-both.sh          # API + worker without opening browser
```

The same commands are also available as `make playground`, `make backend`, `make worker`, and `make both`.

Submit a job:

```bash
curl -X POST http://localhost:8080/submit \
  -H "Content-Type: application/json" \
  -d '{"language": "python", "code": "print(\"hello\")"}'
# returns: {"id": "abc123"}
```

Stream the result:

```bash
curl -N http://localhost:8080/result/abc123
# streams: data: hello\n\ndata: [DONE]\n\n
```

---

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `MAX_WORKERS` | `10` | Size of the goroutine worker pool |
| `DOCKER_TIMEOUT_MS` | `5000` | Max execution time per container in milliseconds |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection string for Pub/Sub |
| `PORT` | `8080` | HTTP server port |
| `DOCKER_MEMORY_LIMIT` | `128m` | Container memory cap |
| `DOCKER_CPU_PERIOD` | `100000` | Docker CPU period for quota enforcement |
| `DOCKER_CPU_QUOTA` | `50000` | CPU quota (50000/100000 = 0.5 core per container) |
| `LANGUAGES_CONFIG` | `languages.json` | JSON file containing language image and command definitions |
| `RATE_LIMIT_RPM` | `60` | Requests per minute per client IP; set `0` to disable |
| `CORS_ALLOWED_ORIGINS` | `*` | Comma-separated allowed origins |
| `CORS_ALLOWED_METHODS` | `GET,POST,OPTIONS` | Comma-separated allowed methods |
| `CORS_ALLOWED_HEADERS` | `Content-Type,Authorization` | Comma-separated allowed headers |
| `PLAYGROUND_ENABLED` | `true` | Serve the static playground at `/playground/` |
| `PLAYGROUND_DIR` | `playground` | Directory used by the static playground server |
| `PRE_PULL_IMAGES` | `true` | Pre-pull sandbox images on worker startup |
| `PRE_PULL_LANGUAGES` | empty | Comma-separated language IDs to pre-pull; empty means all |
| `DATABASE_URL` | empty | Postgres connection string; when set, job history is persisted and `/dashboard/jobs` is enabled |
| `API_KEYS` | empty | Comma-separated API keys; when set, all routes except `/health` and `/playground/*` require a matching `X-API-Key` header or `api_key` query param |
| `DOCKER_RUNTIME` | empty | Docker runtime to use for containers (e.g. `runsc`); empty uses the daemon default |

---

## Sandbox Runtime: gVisor

By default, containers run under the Docker daemon's normal `runc` runtime, which shares the host kernel. For stronger isolation against kernel-level exploits, point the sandbox at [gVisor](https://gvisor.dev)'s `runsc` runtime instead — the application code doesn't need to change, since `internal/sandbox/docker.go` already forwards `DOCKER_RUNTIME` to the container's `HostConfig.Runtime`.

Setup (on the Docker host, not in this repo):

1. Install gVisor: follow the [official install guide](https://gvisor.dev/docs/user_guide/install/) to get the `runsc` binary onto the host.
2. Register it with Docker by adding a runtime entry to `/etc/docker/daemon.json`:
   ```json
   {
     "runtimes": {
       "runsc": {
         "path": "/usr/local/bin/runsc"
       }
     }
   }
   ```
3. Restart the Docker daemon: `sudo systemctl restart docker`.
4. Set `DOCKER_RUNTIME=runsc` in your environment (or uncomment it in `docker-compose.yml`) and restart the worker.

**Tradeoff:** gVisor intercepts syscalls in userspace, which adds CPU/IO overhead per execution in exchange for a much stronger sandbox boundary. Some syscalls used by less common language runtimes may be unsupported — verify with `make verify` after switching.

---

## Design Decisions

### 1. Buffered channels for backpressure over unbounded goroutines

Spawning one goroutine per submission is simple but collapses under load — goroutine count grows with request volume, and each goroutine holds memory and a Docker client handle.

The worker pool with a fixed-size buffered channel inverts this: the pool size is a tunable constant, and the channel provides natural backpressure. When the channel is full, the server returns HTTP 429 immediately instead of silently queuing work it cannot service. This makes load behavior predictable and debuggable.

### 2. Ephemeral containers over persistent runtimes

Persistent sandboxes (e.g., a warm Python interpreter kept alive between submissions) are faster but create isolation risk — state leaks between users, file system contamination, and resource exhaustion within a single container are all real failure modes in a multi-tenant execution environment.

Each submission gets a fresh container from a clean image. The startup cost is the tradeoff; it is paid once per submission and bounded by `DOCKER_TIMEOUT_MS`. The isolation guarantee is unconditional.

### 3. SSE over WebSockets for one-way streaming

WebSockets are full-duplex. For code execution output — which is strictly server-to-client after submission — the bidirectionality is overhead: more complex connection management, more difficult load balancer and proxy configuration, and no benefit for this use case.

SSE is unidirectional, HTTP/1.1-compatible, and trivially handled by every reverse proxy. It delivers the same real-time streaming semantics with lower operational complexity. The reconnection and event ID semantics are also built into the protocol, which WebSockets leave to the application layer.

---

## API Reference

### POST /submit

Submit code for execution.

**Request body:**
```json
{
  "language": "python",
  "code": "print('hello world')",
  "stdin": "",
  "files": {
    "helper.py": "def greet():\n    return 'hi'"
  }
}
```

`code` is written to the language's entrypoint filename (e.g. `solution.py`); `files` is an optional map of additional filenames to contents, written alongside it in the container's working directory.

**Response:**
```json
{
  "id": "abc123",
  "status": "queued"
}
```

**Error responses:**
- `429 Too Many Requests` — worker pool saturated, job rejected
- `400 Bad Request` — unsupported language or malformed body

---

### GET /result/:id

Stream execution output over Server-Sent Events.

**Response:** `Content-Type: text/event-stream`

```
data: hello world\n
\n
data: [DONE]\n
\n
```

Events:
- `data: <output chunk>` — stdout/stderr output as it is produced
- `data: [DONE]` — execution complete
- `data: [ERROR] <message>` — execution failed (timeout, OOM, runtime error)

---

### GET /jobs/:id

Fetch the current record for a job (status, timing, result) without streaming — useful for polling.

**Response:**
```json
{
  "id": "abc123",
  "language": "python",
  "status": "completed",
  "created_at": "2026-07-02T10:00:00Z",
  "result": { "exit_code": 0, "stdout": "hello world\n" }
}
```

`404` if the job ID is unknown.

---

### GET /dashboard/jobs

List recent job records for the dashboard UI. Requires `DATABASE_URL` to be configured.

**Query params:** `limit` (default `50`)

**Response:** `200` with a JSON array of job records, or `501 Not Implemented` if Postgres isn't configured.

---

## Author

**Garv Aggarwal**
Backend Engineer — Java, Go, distributed systems
[github.com/garv2003](https://github.com/garv2003) · [linkedin.com/in/garvaggarwal05](https://linkedin.com/in/garvaggarwal05) · aggarwalgarv0505@gmail.com
