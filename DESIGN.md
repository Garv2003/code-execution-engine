# Design: code-execution-engine

This document describes the architecture, the sandbox isolation model, the
observability surface, and the threat model of the code-execution-engine. It is
written for engineers operating or extending the system.

The sandbox hardening controls (dropped capabilities, `no-new-privileges`, PID
limit, read-only rootfs, bounded output) and the Prometheus metrics are
described here as part of the intended system. Hardening lands on the
`harden-sandbox` branch (PR #2) and the metrics on `b4-metrics`; both are
called out where relevant so this document reflects the target design, not only
what is currently on `main`.

---

## 1. Overview

code-execution-engine runs **untrusted source code** and returns the program's
output. A client submits a snippet (or a set of files) plus optional stdin over
HTTP; the service executes it in a disposable, resource-capped, network-less
container and returns stdout/stderr, the exit code, wall-clock time, peak
memory, and timeout/OOM flags.

The design goals, in priority order:

1. **Isolation** — untrusted code must not affect the host, other jobs, or the
   network.
2. **Bounded resource use** — every run has hard limits on CPU, memory, wall
   time, process count, and output size, so no single job can starve the host.
3. **Horizontal scalability** — the HTTP API and the executors are decoupled
   through a queue and can be scaled independently.

Languages are data-driven: `languages.json` defines 20 runtimes (python,
golang, c, cpp, javascript, typescript/deno, java, rust, ruby, php, bash, perl,
lua, dart, julia, r, elixir, erlang, clojure, haskell). Each entry declares the
Docker image, source filename, optional compile command, run command,
per-language timeout, memory limit, and a health-check command. Adding a
language is a config change, not a code change.

---

## 2. Architecture

### Data flow

```
                          ┌──────────────────────────────────────────────┐
   HTTP client            │                 API process                  │
      │                   │  (APP_MODE=api or both)                      │
      │  POST /submit     │                                              │
      ├──────────────────▶│  SubmitHandler                               │
      │                   │   1. validate language + payload             │
      │                   │   2. clamp timeout to per-language max        │
      │                   │   3. StoreJobRecord (Redis, status=queued)    │
      │                   │   4. UpsertJobRecord (Postgres, optional)     │
      │                   │   5. PushJob  ──LPUSH cee:job_queue──┐        │
      │  202 {id,queued}  │                                     │        │
      │◀──────────────────┤                                     │        │
      │                   └─────────────────────────────────────┼────────┘
      │                                                          ▼
      │                                              ┌───────────────────────┐
      │                                              │        Redis          │
      │                                              │  list  cee:job_queue  │
      │                                              │  kv    cee:job:{id}    │
      │                                              │  kv    cee:result:{id} │
      │                                              │  pub   result:{id}     │
      │                                              └───────────┬───────────┘
      │                                        BRPOP cee:job_queue│
      │                                                          ▼
      │                   ┌──────────────────────────────────────────────┐
      │                   │              Worker process                  │
      │                   │  (APP_MODE=worker or both)                   │
      │                   │  WorkerPool: N goroutines, each:             │
      │                   │   1. BRPOP a job (blocking)                   │
      │                   │   2. mark record running (Redis + Postgres)  │
      │                   │   3. sandbox.Run(job)  ─────────────┐         │
      │                   │   4. mark completed/failed           │        │
      │                   │   5. Set cee:result:{id} (15m TTL)   │        │
      │                   │   6. PUBLISH result:{id}             │        │
      │                   └──────────────────────────────────────┼───────┘
      │                                                           ▼
      │                                            ┌────────────────────────────┐
      │                                            │      Docker daemon         │
      │                                            │  one fresh container /run  │
      │                                            │  NetworkMode=none, cgroups │
      │                                            └────────────────────────────┘
      │  GET /result/{id}  (SSE)
      └──────────────────▶ ResultHandler
                             - cached result present?  → emit once, close
                             - else SUBSCRIBE result:{id}, stream event when
                               the worker publishes, then close
```

### Request lifecycle in prose

1. **Submit.** `POST /submit` validates the language against `languages.json`
   and requires `code` or `files`. The requested `timeout_ms` is clamped to the
   per-language maximum (default 5 s). A UUID job ID is generated, a `JobRecord`
   is written to Redis (`cee:job:{id}`, status `queued`, 24 h TTL) and, if
   Postgres is configured, upserted there. The full `Job` is `LPUSH`ed onto the
   Redis list `cee:job_queue`. The handler returns `202 Accepted` with
   `{id, status:"queued"}` — it never blocks on execution.

2. **Queue.** `cee:job_queue` is a plain Redis list used as a FIFO work queue.
   Producers `LPUSH`; workers `BRPOP` (blocking pop) from the other end.

3. **Worker.** `WorkerPool` starts `MAX_WORKERS` goroutines (default 10). Each
   loops on `BRPOP`, marks the record `running` (persisting to Redis and
   Postgres), and calls `sandbox.Run(job)` with a context deadline equal to the
   job's timeout.

4. **Sandbox.** The sandbox creates one fresh container per run, copies the
   source in, streams stdin/stdout/stderr, waits for exit (or timeout/OOM), and
   force-removes the container. See §3.

5. **Result cache + publish.** The worker classifies the outcome
   (completed / failed / timeout / OOM), updates the record, writes the result
   to `cee:result:{id}` with a 15 min TTL, and `PUBLISH`es it to the pub/sub
   channel `result:{id}`.

6. **Deliver over SSE.** `GET /result/{id}` sets `text/event-stream`. If a
   cached result already exists it is emitted immediately and the stream closes.
   Otherwise the handler `SUBSCRIBE`s to `result:{id}`, sends a `subscribed`
   keepalive event, and forwards the result as a single SSE `data:` event when
   the worker publishes, then closes. The cache-first path means a client that
   reconnects after the job finished still gets the answer (within the 15 min
   TTL) instead of hanging on a channel that already fired.

Job status and full history can also be polled synchronously: `GET /jobs/{id}`
returns the current `JobRecord` from Redis, and `GET /dashboard/jobs` returns
recent jobs from Postgres (when configured).

### Deployment modes and scaling

A single binary runs in one of three modes via `APP_MODE`:

| Mode     | Runs                                  | Use                                            |
|----------|---------------------------------------|------------------------------------------------|
| `api`    | HTTP server only                      | Stateless front tier behind a load balancer.   |
| `worker` | Worker pool only (needs Docker)       | Executor tier, scaled to match Docker capacity.|
| `both`   | API + workers in one process (default)| Single-node / local development.               |

Because the API and workers communicate only through Redis, the two tiers scale
**independently and horizontally**: run many `api` replicas behind a load
balancer for request throughput, and many `worker` replicas (each on a host with
a Docker daemon) for execution throughput. Redis is the shared coordination
point — the queue, the result cache, and the pub/sub bus. SSE delivery works
across replicas because the result is published to Redis pub/sub, so any API
replica holding the client connection receives it, regardless of which worker
ran the job. `MAX_WORKERS` bounds the concurrent containers per worker process.

---

## 3. Execution sandbox

The unit of isolation is **one fresh container per run** (`internal/sandbox/docker.go`).
Nothing is reused between jobs: the container is created, the code is copied in,
the process runs, and the container is force-removed in a `defer` regardless of
outcome. This gives every job a clean filesystem and process table and removes
any chance of state leaking between untrusted runs.

The container is created from the language's image with `Cmd = ["sh", "-c",
"<compile> && <run>"]` (or just the run command when there is no compile step),
`WorkingDir=/app`, and stdin/stdout/stderr attached.

### Isolation and resource controls

- **Linux namespaces + cgroups.** Standard container isolation — the process
  gets its own PID, mount, network, IPC, and UTS namespaces and a cgroup for
  resource accounting, courtesy of the Docker daemon.

- **No network.** `NetworkMode: "none"`. The container has only a loopback
  interface; there is no route off the box and no DNS. Untrusted code cannot
  reach the internet, internal services, or the metadata endpoint.

- **Memory limit.** `Resources.Memory` is set from the per-language `memory_mb`
  (default 128 MB) or `DOCKER_MEMORY_LIMIT`. `OomKillDisable` is explicitly
  `false`, so the kernel OOM-kills a process that exceeds the cgroup limit.

- **CPU quota.** `CPUPeriod` / `CPUQuota` (defaults 100000 / 50000 → ~0.5 CPU)
  cap CPU time via the CFS bandwidth controller. Compute-bound code is throttled,
  not allowed to monopolize a core.

- **Wall-clock timeout.** The worker calls `sandbox.Run` with a context whose
  deadline is the job timeout. On `ctx.Done()` the sandbox issues
  `ContainerStop` with timeout 0 (immediate kill) and returns a result with
  `Timeout: true`. This is the backstop for code that neither exits nor trips a
  resource limit (e.g. `sleep`, a blocking read, a busy loop under the CPU cap).

- **OOM detection.** After the container exits, `ContainerInspect` is read and
  `State.OOMKilled` is surfaced as `result.OOM`, which the worker maps to a
  `failed` record with error `out of memory` — distinct from a normal non-zero
  exit.

- **Peak memory sampling.** A goroutine streams `ContainerStats` and tracks the
  max `memory_stats.usage`, reported as `memory_used_bytes` for observability
  (not an enforcement mechanism — enforcement is the cgroup limit above).

- **PID limit** *(hardening)*. `PidsLimit` (`DOCKER_PIDS_LIMIT`, default 256)
  caps the number of processes/threads in the container's cgroup, so a fork bomb
  cannot exhaust host PIDs.

- **Dropped capabilities + no privilege escalation** *(hardening)*.
  `CapDrop: ["ALL"]` removes every Linux capability, and
  `SecurityOpt: ["no-new-privileges"]` prevents regaining privilege via setuid
  binaries. Code runs with the minimum kernel-level authority.

- **Optional read-only rootfs** *(hardening)*. With
  `DOCKER_READONLY_ROOTFS=true` the container root filesystem is mounted
  read-only and a size-bounded `tmpfs` is mounted at `/tmp`
  (`DOCKER_TMPFS_SIZE_MB`, default 64 MB). This stops writes outside the
  scratch area and bounds scratch disk. It is off by default because several
  bundled languages compile to disk under `/app`.

- **Bounded stdout/stderr** *(hardening)*. Output is captured through a
  `cappedBuffer` limited to `MAX_OUTPUT_BYTES` (default 1 MiB) per stream.
  Excess bytes are dropped and `OutputTruncated` is set, so a process printing
  unbounded output cannot exhaust worker memory or wedge the pipe.

- **Optional gVisor.** Setting `DOCKER_RUNTIME=runsc` runs containers under the
  gVisor runtime instead of the host kernel's runc. gVisor interposes a
  user-space kernel between the workload and the host, shrinking the host kernel
  attack surface. This requires gVisor to be installed and registered as a
  Docker runtime on the host.

### Input handling

Source is delivered into the container as an in-memory tar archive extracted to
`/app`: a single `code` string is written as the language's `filename`, and any
entries in the `files` map are written alongside it. If `stdin` is provided it is
written to the attached stdin stream and the write side is then closed
(`CloseWrite`) so programs reading to EOF terminate.

---

## 4. Observability

The service exposes Prometheus metrics at `GET /metrics` (registered on the API
mux; see `internal/metrics/metrics.go` on `b4-metrics`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `cee_job_execution_seconds` | Histogram (default buckets) | — | Wall-clock duration of sandbox execution per job. Drives latency percentiles and timeout tuning. |
| `cee_jobs_total` | Counter | `outcome` = `completed` \| `failed` \| `timeout` \| `oom` | Processed-job count by terminal outcome. All four label values are pre-initialized so rates are well-defined from zero. |
| `cee_queue_depth` | Gauge | — | Current length of `cee:job_queue` (Redis `LLEN`), sampled periodically by the worker pool. The primary backpressure/saturation signal. |

Typical use: alert on rising `cee_queue_depth` (workers can't keep up →
scale the worker tier), track the `timeout`/`oom` share of `cee_jobs_total` (bad
inputs or limits too tight), and watch `cee_job_execution_seconds` high
percentiles against the configured timeout. Structured JSON logs (slog) complement
the metrics with per-job `job_id` / `worker_id` context.

---

## 5. Threat model

Assumption: **the submitted code is hostile.** The trust boundary is the
container; everything inside it is attacker-controlled. Controls marked
*(hardening)* ship on `harden-sandbox` / PR #2.

| Threat | Control | Residual risk |
|---|---|---|
| **Fork bomb / process exhaustion** | `PidsLimit` (default 256) caps processes per cgroup *(hardening)*; CPU quota throttles spawn rate; per-run container is discarded. | Within the PID budget a job can still churn short-lived processes; tune the limit down for hostile multi-tenant use. |
| **Infinite / huge output** | `cappedBuffer` truncates each stream at `MAX_OUTPUT_BYTES` (default 1 MiB) and sets `OutputTruncated` *(hardening)*; result cache carries a 15 min TTL. | Truncation loses tail output; a job can still produce up to the cap per run. |
| **Network exfiltration / SSRF** | `NetworkMode: "none"` — no interfaces beyond loopback, no DNS, no route to host/metadata/internal services. | None via the container network. Any exfil path would require a separate escape first. |
| **Filesystem escape / host tampering** | Namespaced mount view; per-run container force-removed; `CapDrop: ["ALL"]` + `no-new-privileges` *(hardening)*; optional read-only rootfs + size-bounded `/tmp` tmpfs *(hardening)*. | Read-only rootfs is off by default (compiled languages write to `/app`); with a writable rootfs a job can fill the container layer up to disk/tmpfs limits, though it is discarded at exit. |
| **CPU / memory exhaustion (host DoS)** | Per-run cgroup memory limit with kernel OOM-kill (`OomKillDisable=false`); `CPUPeriod`/`CPUQuota` bandwidth cap; `MAX_WORKERS` bounds concurrent containers per worker. | Aggregate load across many concurrent jobs can still pressure the host; size `MAX_WORKERS` and worker replicas to host capacity. |
| **Privilege escalation** | `CapDrop: ["ALL"]` drops all capabilities; `SecurityOpt: ["no-new-privileges"]` blocks setuid re-escalation *(hardening)*; optional gVisor (`runsc`) interposes a user-space kernel. | A host-**kernel** 0-day (container escape) remains the dominant risk under default runc — the shared-kernel caveat. gVisor mitigates but does not eliminate it and adds its own (smaller) attack surface. |
| **Long-running / hung job** | Context-deadline wall-clock timeout → immediate `ContainerStop`; timeout returned as a distinct `timeout` outcome. | Timeout is enforced by the worker; a stuck Docker daemon or `ContainerStop` failure could delay reclamation (see §6). |

### Honest residual risks

- **Shared kernel.** Under the default runc runtime, all containers share the
  host kernel. A kernel privilege-escalation or container-escape 0-day defeats
  namespace isolation. `DOCKER_RUNTIME=runsc` (gVisor) is the mitigation for
  high-assurance deployments; it reduces but does not remove kernel-level risk.
- **Supply chain of base images.** The 20 runtime images are pulled from public
  registries. A compromised or typo-squatted image would run with whatever the
  sandbox permits. Pin digests, mirror to a trusted registry, and scan images.
- **Denial of service at the queue/API tier.** The per-IP fixed-window rate
  limiter and optional API-key auth throttle submission, but a determined
  authenticated client can still fill the queue; capacity planning and the
  `cee_queue_depth` signal are the operational backstop.
- **gVisor is not a panacea.** It shrinks the host-kernel surface but introduces
  its own sentry/gofer surface and does not cover every syscall identically to
  the host kernel.

---

## 6. Scaling and limitations

### Bottlenecks

- **Docker daemon throughput.** Each run is a create → copy → start → wait →
  remove cycle against the local Docker daemon. The daemon, not Go, is the
  practical ceiling on per-worker throughput; container create/teardown and
  image layer setup dominate short jobs. The daemon is also a single point of
  failure per worker host — if it hangs, that worker stalls.
- **Per-run container cost.** Creating and destroying a container per job trades
  throughput for isolation. Cold image pulls add latency; `PRE_PULL_IMAGES`
  warms configured images on worker start, and the standalone `verify` command
  checks each image and runs its health-check to catch a broken runtime before
  it serves traffic.
- **Redis as a shared dependency.** The queue, result cache, and pub/sub all sit
  on Redis. It is the coordination hub and a single point of failure for the
  whole system; it must be sized and made highly available for the target load.
- **In-process rate limiter.** The per-IP fixed-window limiter is per API
  replica (in-memory `sync.Map`), so the effective global limit scales with
  replica count rather than being a true cluster-wide cap.

### Future work

- Pooled or pre-warmed sandboxes (or a microVM backend such as Firecracker) to
  cut per-run container cost while keeping per-job isolation.
- Native/non-Docker execution backend (there is a `native-sandbox` branch)
  behind the same `sandbox` interface.
- Redis high-availability / a durable queue, plus dead-letter handling for jobs
  that repeatedly fail to execute.
- A distributed (Redis-backed) rate limiter for a true cluster-wide cap.
- Per-tenant quotas and richer accounting keyed off the metrics above.
