#!/usr/bin/env sh

set -eu

. "$(dirname -- "$0")/common.sh"

printf 'Starting API and worker on http://localhost:%s\n' "$PORT"
run_server both
