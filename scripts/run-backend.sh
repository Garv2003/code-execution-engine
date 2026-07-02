#!/usr/bin/env sh

set -eu

. "$(dirname -- "$0")/common.sh"

printf 'Starting API only on http://localhost:%s\n' "$PORT"
run_server api
