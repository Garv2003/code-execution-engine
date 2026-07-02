#!/usr/bin/env sh

set -eu

. "$(dirname -- "$0")/common.sh"

url="http://localhost:${PORT}/playground/"
printf 'Starting API and worker on %s\n' "$url"

(
	sleep 2
	open_url "$url"
) &

run_server both
