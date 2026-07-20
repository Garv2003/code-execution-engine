# code-execution-engine

A self-hostable service that runs untrusted source code inside disposable Docker containers and returns the program's output. Clients submit a code snippet (or a set of files) plus optional stdin over HTTP; the API enqueues the job, a pool of workers executes it in an isolated, resource-capped, network-less container, and the result is streamed back to the client over Server-Sent Events. It is built around a Redis-backed job queue so the HTTP API and the executors can be scaled and run as separate processes.

See [DESIGN.md](DESIGN.md) for the architecture, sandbox isolation model, and threat model.

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
| `DOCKER_CPU_PERIOD` / `DOCKER_CPU_QUOTA` | `100000` / `50000` | CPU throttling |
| `DOCKER_RUNTIME` | _(empty)_ | e.g. `runsc` for gVisor |
| `RATE_LIMIT_RPM` | `60` | Per-IP requests/minute (0 disables) |
| `API_KEYS` | _(empty)_ | Comma-separated keys (empty disables auth) |
| `CORS_ALLOWED_ORIGINS` / `_METHODS` / `_HEADERS` | `*` / `GET,POST,OPTIONS` / `Content-Type,Authorization` | CORS policy |
| `PRE_PULL_IMAGES` | `true` | Pre-pull images on worker start |
| `PRE_PULL_LANGUAGES` | _(all)_ | Subset to pre-pull |
| `PLAYGROUND_ENABLED` / `PLAYGROUND_DIR` | `true` / `playground` | Static playground |

### Endpoints

| Method & path | Description |
|---|---|
| `GET /health` | Liveness probe, returns `OK` |
| `POST /submit` | Enqueue a job; returns `202 {"id","status":"queued"}` |
| `GET /result/{id}` | SSE stream; emits the execution result as `data: <json>` |
| `GET /jobs/{id}` | Current job record (status/result) as JSON |
| `GET /dashboard/jobs?limit=N` | Recent jobs from Postgres (requires `DATABASE_URL`; else `501`) |
| `GET /playground/` | Static playground UI (when enabled) |

### Submit example

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
