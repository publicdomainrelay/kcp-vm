#!/usr/bin/env bash
set -euo pipefail

name="${1:?usage: wait-ready.sh <container-name> [timeout-seconds]}"
timeout="${2:-60}"

start=$(date +%s)

while :; do
	if ! running=$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null); then
		echo "wait-ready: no container $name" >&2
		exit 2
	fi

	last="$(docker logs "$name" 2>&1 | grep -F '"msg":"cluster provider: status"' | tail -n1 || true)"
	if [ -n "$last" ] && printf '%s' "$last" | grep -qF '"phase":"Ready"'; then
		echo "wait-ready: $name ready in $(( $(date +%s) - start ))s"
		exit 0
	fi

	if [ "$running" != "true" ]; then
		echo "wait-ready: $name exited before ready" >&2
		docker logs --tail 40 "$name" >&2 2>&1
		exit 1
	fi

	if [ $(( $(date +%s) - start )) -ge "$timeout" ]; then
		echo "wait-ready: $name not ready after ${timeout}s" >&2
		docker logs --tail 40 "$name" >&2 2>&1
		exit 1
	fi

	sleep 1
done
