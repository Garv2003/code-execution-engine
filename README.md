# code-execution-engine

A self-hostable service that runs untrusted source code inside disposable Docker containers and returns the program's output. Clients submit a code snippet (or a set of files) plus optional stdin over HTTP; the API enqueues the job, a pool of workers executes it in an isolated, resource-capped, network-less container, and the result is streamed back to the client over Server-Sent Events. It is built around a Redis-backed job queue so the HTTP API and the executors can be scaled and run as separate processes.

See [DESIGN.md](DESIGN.md) for the architecture, sandbox isolation model, and threat model. For throughput/latency load testing, see [bench/README.md](bench/README.md).

## Features

- **Multi-language execution** driven by a JSON config file. The bundled `languages.json` defines 20 runtimes: python, golang, c, cpp, javascript, node, typescript (deno), java, rust, ruby, php, bash, perl, lua, dart, julia, r, elixir, erlang, clojure, haskell. Each entry specifies the Docker image, source filename, optional compile command, run command, per-language timeout, memory limit, and a health-check command.
- **Container isolation per run.** Every job runs in a fresh container that is force-removed afterward. Containers run with `NetworkMode: none` (no network access), a memory limit, and CPU quota/period limits.
- **Resource controls & signals.** Enforced wall-clock timeout (via context deadline + `ContainerStop`), memory limit, CPU throttling, and OOM detection read from container inspect state. The result reports stdout, stderr, exit code, wall time, peak memory (sampled from the container stats stream), and `timeout`/`oom` flags.
- **stdin and multi-file support.** A request may include a single `code` string and/or a `files` map (filename → contents); all are written into `/app` inside the container via a tar archive, and `stdin` is piped to the process.
- **Async job model.** `POST /submit` returns immediately with a job ID and `queued` status; results are retrieved separately.
- **Result delivery over SSE.** `GET /result/{id}` returns a cached result immediately if present, otherwise subscribes to a Redis pub/sub channel and streams the result event when the worker finishes.
- **Job status tracking.** Job records (queued → running → completed/failed) are stored in Redis with a 24h TTL; results are cached with a 15m TTL.
- **Optional Postgres persistence** for job history. When `DATABASE_URL` is set, job records are upserted into a `jobs` table (auto-created at startup) and exposed through `GET /dashboard/jobs`.
- **Middleware chain:** optional API-key auth (header or query param, with public paths exempt), configurable CORS, and a per-IP fixed-window rate limiter.
- **Split or combined deployment.** A single binary runs as `api`, `worker`, or `both` via `APP_MODE`.
- **Image pre-pulling and runtime verification.** Optional background pre-pull of configured images on worker start, plus a standalone `verify` command that ensures each image is present and runs its health-check command.
- **Static playground** served from `/playground/` for interacting with the API from a browser.
- **Optional gVisor sandboxing** by setting `DOCKER_RUNTIME=runsc` (requires gVisor installed and registered as a Docker runtime on the host).

## How it works

### Request / data flow

```
POST /submit  ──►  SubmitHandler
                     • validate language + code/files
                     • resolve effective timeout (request vs. per-language cap)
                     • create Job + JobRecord (status=queued)
                     • StoreJobRecord in Redis (+ Postgres if configured)
                     • PushJob → Redis list "cee:job_queue"
                   returns 202 {"id","status":"queued"}

Worker pool  ──►  BRPop "cee:job_queue" (blocking)
                     • mark record running (Redis + Postgres)
                     • DockerSandbox.Run(job) with per-job timeout context
                     • mark record completed/failed with result
                     • Set "cee:result:{id}" (15m TTL)
                     • PUBLISH "result:{id}" with the result JSON

GET /result/{id} ─► ResultHandler (SSE)
                     • if "cee:result:{id}" exists → emit once, close
                     • else SUBSCRIBE "result:{id}", emit on first message
```

### Sandbox execution (`internal/sandbox/docker.go`)

For each job the sandbox: ensures the image exists (pulling on demand), creates a container running `sh -c "<compile> && <run>"` (or just the run command when there is no compile step) with `WorkingDir=/app`, copies the source/files in as a tar archive, attaches stdin/stdout/stderr, starts the container, and waits on `ContainerWait`. Output is demultiplexed with Docker's `stdcopy`. A background goroutine tails the container stats stream to record peak memory. If the job context deadline fires first, the container is stopped and the result is flagged as a timeout. OOM state is read from `ContainerInspect`.

### Concurrency model

- `WorkerPool` (`internal/worker/worker.go`) launches `MAX_WORKERS` goroutines, each looping on a blocking `BRPop` and processing one job at a time. A `shutdown` channel plus `sync.WaitGroup` provide graceful drain on `SIGINT`/`SIGTERM`, bounded by a 10s context.
- The HTTP server runs concurrently; `main` blocks on the signal channel, then shuts down the API server (10s grace) and the worker pool. A second signal forces exit.
- The rate limiter uses a `sync.Map` of per-IP atomic counters with a background sweeper goroutine.

### Key packages

- `cmd/server` — process entrypoint; wires config, Redis, optional Postgres, sandbox, worker pool, HTTP routes, and middleware based on `APP_MODE`.
- `cmd/verify` — standalone runtime health checker.
- `internal/config` — environment-variable configuration loader with validation.
- `internal/sandbox` — Docker client, language config loading, container run/verify/pre-pull.
- `internal/worker` — worker pool consuming the queue and driving the sandbox.
- `internal/pushsub` — Redis client: job queue, job records, result cache, pub/sub.
- `internal/db` — optional Postgres persistence (schema bootstrap + upsert + recent-jobs query).
- `internal/handler` — HTTP handlers: `submit`, `result` (SSE), `job`, `health`, `dashboard`.
- `internal/middleware` — API key, CORS, rate limiting.
- `internal/models` — `Job`, `JobRecord`, `ExecutionResult`, `JobStatus`.

## Tech stack

- **Go** (module targets `go 1.25.0`), standard-library `net/http` router using Go 1.22+ method+path patterns (e.g. `POST /submit`, `GET /result/{id}`).
- **Docker SDK for Go** — `github.com/docker/docker v28.5.2+incompatible`, `github.com/docker/go-units`.
- **Redis** — `github.com/redis/go-redis/v9 v9.21.0` (job queue + pub/sub + caching).
- **PostgreSQL** (optional) — `github.com/lib/pq v1.12.3`, via `database/sql`.
- **Config** — `github.com/joho/godotenv v1.5.1` for `.env` loading; structured logging via `log/slog` (JSON handler).
- **Runtime dependency:** a reachable Docker daemon (configured via `DOCKER_*` / `client.FromEnv`) and a Redis instance.

## Getting started

Prerequisites: Go, a running Docker daemon, and Redis.

```bash
# 1. Configure (optional; sane defaults exist)
cp .env.example .env

# 2. Start Redis (helper target uses a local redis:alpine container)
make redis-start

# 3. Run API + worker in one process
make run          # go run ./cmd/server  (APP_MODE defaults to "both")

# Build binaries (server + verify) into ./bin
make build

# Verify that all configured runtime images are present and healthy
make verify       # go run ./cmd/verify

# Run tests (race detector enabled) / go vet
make test
make lint
```

Helper scripts (used by the Make targets below) set development-friendly env defaults and default to `PORT=7800`:

```bash
make backend      # API only
make worker       # worker only
make both         # API + worker
make playground   # API + worker, then opens http://localhost:<PORT>/playground/
```

### Docker Compose

`docker-compose.yml` brings up Redis, an `api` service (`APP_MODE=api`, port 8080), and a `worker` service (`APP_MODE=worker`) that mounts the host Docker socket so it can launch sandbox containers:

```bash
docker compose up --build
```

## Usage

### Configuration (environment variables)

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | API listen port |
| `APP_MODE` | `both` | `api`, `worker`, or `both` |
| `MAX_WORKERS` | `10` | Worker goroutines |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection |
| `DATABASE_URL` | _(empty)_ | Postgres DSN; enables history + dashboard |
| `LANGUAGES_CONFIG` | `languages.json` | Path to language definitions |
| `DOCKER_TIMEOUT_MS` | `5000` | Default execution timeout |
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
| `SANDBOX_BACKEND` | `docker` | `docker` or `native` (experimental) — see "Sandbox Backends: Native" below |
| `NATIVE_WORKDIR` | OS temp dir | Native backend only — base directory for per-job temp directories |
| `NATIVE_UID` / `NATIVE_GID` | `0` | Native backend only — UID/GID to drop the sandboxed process to; requires the worker to run as root |

---

## Sandbox Backends: Native (Experimental)

`SANDBOX_BACKEND=native` runs jobs directly on the host instead of in Docker containers — no daemon, no `docker.sock`, no images. It isolates each job using Linux namespaces (PID, mount, network, UTS, IPC) and enforces memory/CPU/PID-count limits via cgroups v2, then executes the language's existing `run_command`/`compile_command` from `languages.json` directly against the host's installed toolchains.

**This is experimental and has only been verified to compile (including cross-compiled for Linux), not run end-to-end** — this repo is developed on macOS, which has no namespaces/cgroups to test against. Try it on a disposable Linux box before trusting it with real traffic.

**Read before using — this is not a drop-in replacement for the Docker backend:**
- **No filesystem jail.** Unlike Docker, there's no chroot or per-language rootfs — sandboxed code can read most of the host filesystem, subject to normal Unix file permissions. It's contained on process tree, network, and resource axes, not filesystem visibility.
- **Requires toolchains on the host.** Since there's no per-language container image, whatever `languages.json` expects (`python3`, `gcc`, `node`, etc.) must already be installed on the machine running the worker.
- **Requires cgroup v2 delegation.** The worker needs write access to `/sys/fs/cgroup/cee` — either run it as root, or delegate that subtree to its user (e.g. via a systemd unit with `Delegate=yes`).
- **Linux only.** `internal/sandbox/native_linux.go` is behind a `//go:build linux` tag; on any other OS, selecting this backend fails fast at startup with a clear error.

Use `NATIVE_UID`/`NATIVE_GID` to drop the sandboxed process to an unprivileged, dedicated system user before exec — this is the main mitigation for the lack of a filesystem jail, so don't run the worker (or the sandboxed code) as root in practice without it.

When it's a good fit: lower per-job overhead (no container create/start/teardown, no image pulls) for trusted or semi-trusted workloads where you control what languages/code run. When it isn't: multi-tenant, untrusted, public-facing execution — use the Docker backend (optionally with gVisor, below) there instead.

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

```bash
curl -X POST http://localhost:8080/submit \
  -H 'Content-Type: application/json' \
  -d '{
        "language": "python",
        "code": "print(input())",
        "stdin": "hello",
        "timeout_ms": 3000
      }'
# → {"id":"<uuid>","status":"queued"}

curl -N http://localhost:8080/result/<uuid>
# → data: {"id":"...","stdout":"hello\n","stderr":"","exit_code":0,"time_used":...,"memory_used_bytes":...,"timeout":false,"oom":false}
```

The effective timeout is the smaller of the requested `timeout_ms` and the per-language cap in `languages.json`. When `API_KEYS` is set, pass the key via the `X-API-Key` header or `?api_key=` query parameter (the `/health` and `/playground` paths remain public).

## Project structure

```
code-execution-engine/
├── cmd/
│   ├── server/main.go        # entrypoint: config, wiring, routes, graceful shutdown
│   └── verify/main.go        # image presence + health-check verification tool
├── internal/
│   ├── config/               # env-var config loader + validation
│   ├── db/                   # optional Postgres persistence (schema, upsert, queries)
│   ├── handler/              # HTTP handlers: submit, result(SSE), job, health, dashboard
│   ├── middleware/           # API key, CORS, per-IP rate limiter
│   ├── models/               # Job, JobRecord, ExecutionResult, JobStatus
│   ├── pushsub/              # Redis: queue, records, result cache, pub/sub
│   ├── sandbox/              # Docker sandbox: run, pre-pull, verify, language config
│   └── worker/               # worker pool consuming the queue
├── playground/               # static browser UI (index.html, dashboard.html, config.json)
├── scripts/                  # run-backend / run-worker / run-both / start-playground helpers
├── docker/Dockerfile         # container image for api/worker
├── docker-compose.yml        # redis + api + worker
├── languages.json            # runtime definitions (image, commands, limits, health check)
├── Makefile                  # build/run/verify/test/lint/redis helpers
└── .env.example              # documented configuration template
```
