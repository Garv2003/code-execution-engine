# Load-testing harness

A small stdlib-only load generator for the code-execution-engine. Each worker
loops: `POST /submit` with a tiny sample program, then `GET /result/{id}` and
consumes the SSE stream until the terminal result event arrives, recording the
full submit-to-result end-to-end latency and whether the run succeeded (clean
exit, no timeout, no OOM).

## Prerequisites — a live instance

The harness needs a running engine (API + worker) plus Redis and a reachable
Docker daemon. Bring the stack up the same way the [main README](../README.md)
describes:

```bash
# from the repo root
docker compose up --build      # redis + api (:8080) + worker
```

or run it locally without compose:

```bash
make redis-start               # local redis:alpine container
make run                       # API + worker in one process (APP_MODE=both)
```

Note the port: `docker compose` exposes the API on `:8080` (the harness
default), while `make run` / the helper scripts default to `PORT=7800` — pass
`-url http://localhost:7800` in that case.

## Running

```bash
# from the repo root

# time-bounded run: 20 concurrent workers for 30 seconds
go run ./bench -concurrency 20 -duration 30s

# fixed total number of requests
go run ./bench -concurrency 20 -requests 500

# a different language (must exist in languages.json)
go run ./bench -lang javascript -concurrency 10 -duration 30s

# against a remote / auth-protected instance
go run ./bench -url https://exec.example.com -api-key "$API_KEY" -concurrency 20 -duration 30s
```

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `-url` | `http://localhost:8080` | Base URL of the API |
| `-lang` | `python` | Language id to submit (built-in samples: python, golang, javascript, ruby, php, bash, perl, lua; others fall back to the python program) |
| `-concurrency` | `10` | Number of concurrent workers |
| `-duration` | _(unset)_ | How long to run, e.g. `30s`, `2m` |
| `-requests` | _(unset)_ | Total requests to send instead of running for a duration |
| `-api-key` | _(empty)_ | Sent via the `X-API-Key` header when the engine has `API_KEYS` configured |

`-duration` and `-requests` are mutually exclusive; provide exactly one.
`Ctrl-C` cancels early and still prints the stats gathered so far.

### Output

The harness prints total requests, successes/failures, error rate, throughput
(req/s over wall time), and p50/p95/p99/min/max end-to-end latency. Percentiles
are computed by sorting the collected samples and indexing (nearest-rank).

## Reading `/metrics`

The engine exposes Prometheus metrics at `GET /metrics`. While a run is in
flight, scrape it to correlate the client-side numbers above with server-side
behaviour:

```bash
curl -s http://localhost:8080/metrics | grep -E '^cee_|^# HELP cee_'
```

Useful series (see `internal/metrics`):

- `cee_jobs_total{outcome="completed|failed|timeout|oom"}` — job outcome counts;
  compare `failed`/`timeout`/`oom` against the harness error rate.
- `cee_job_execution_seconds` — histogram of in-sandbox execution time. This is
  the container run time only; the harness latency additionally includes queue
  wait + submit/SSE round-trips, so harness p95 ≥ this histogram's p95.
- `cee_queue_depth` — current job-queue depth; if this climbs during a run, the
  worker pool (`MAX_WORKERS`) is saturated and throughput is queue-bound.

## Results

**Numbers must be measured against a live instance — do not fill these in from
guesses.** Run the harness at each concurrency level and paste the printed
values here.

_Measured on `<machine>` (CPU / RAM), `<language>`, engine `<git sha>`,
`MAX_WORKERS=<n>` — fill after running._

| Concurrency | Throughput (req/s) | p50 | p95 | p99 | Error % |
|---|---|---|---|---|---|
| 1   | _tbd_ | _tbd_ | _tbd_ | _tbd_ | _tbd_ |
| 10  | _tbd_ | _tbd_ | _tbd_ | _tbd_ | _tbd_ |
| 20  | _tbd_ | _tbd_ | _tbd_ | _tbd_ | _tbd_ |
| 50  | _tbd_ | _tbd_ | _tbd_ | _tbd_ | _tbd_ |
