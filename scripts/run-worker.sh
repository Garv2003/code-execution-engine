#!/usr/bin/env sh

set -eu

. "$(dirname -- "$0")/common.sh"

printf 'Starting worker only\n'
run_server worker
