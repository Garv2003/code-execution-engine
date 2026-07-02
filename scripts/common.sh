#!/usr/bin/env sh

set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

export PORT="${PORT:-7800}"
export REDIS_URL="${REDIS_URL:-redis://localhost:6379}"
export LANGUAGES_CONFIG="${LANGUAGES_CONFIG:-languages.json}"
export PLAYGROUND_DIR="${PLAYGROUND_DIR:-playground}"
export PLAYGROUND_ENABLED="${PLAYGROUND_ENABLED:-true}"
export RATE_LIMIT_RPM="${RATE_LIMIT_RPM:-120}"
export CORS_ALLOWED_ORIGINS="${CORS_ALLOWED_ORIGINS:-*}"
export MAX_WORKERS="${MAX_WORKERS:-10}"
export DOCKER_TIMEOUT_MS="${DOCKER_TIMEOUT_MS:-5000}"
export DOCKER_MEMORY_LIMIT="${DOCKER_MEMORY_LIMIT:-128m}"
export PRE_PULL_IMAGES="${PRE_PULL_IMAGES:-false}"
export PRE_PULL_LANGUAGES="${PRE_PULL_LANGUAGES:-python,golang,cpp,c,javascript,typescript,java,rust,ruby,php}"

ensure_port_available() {
	port="$1"
	if command -v lsof >/dev/null 2>&1 && lsof -iTCP:"$port" -sTCP:LISTEN -n -P >/dev/null 2>&1; then
		printf 'Port %s is already in use. Stop that process or run with another port, for example:\n' "$port" >&2
		printf '  PORT=7800 %s\n' "$0" >&2
		exit 1
	fi
}

open_url() {
	url="$1"
	if command -v open >/dev/null 2>&1; then
		open "$url" >/dev/null 2>&1 || true
	elif command -v xdg-open >/dev/null 2>&1; then
		xdg-open "$url" >/dev/null 2>&1 || true
	else
		printf 'Open this URL manually: %s\n' "$url"
	fi
}

run_server() {
	APP_MODE="$1"
	export APP_MODE
	if [ "$APP_MODE" = "api" ] || [ "$APP_MODE" = "both" ]; then
		ensure_port_available "$PORT"
	fi
	exec go run ./cmd/server
}
