#!/usr/bin/env bash
# Starts an image in demo mode and waits up to 30 s for / and /api/site.json
# to answer 200. The requests run inside the container (BusyBox wget), so the
# check works whatever network a CI job runs in. Prints the logs on failure
# and always removes the container.
#
# It runs the image locked down, as the install guide asks: a read-only root
# file system, no Linux capabilities, no new privileges. ForgeMap writes only
# to /data; the demo also writes its fake world under /tmp, hence the tmpfs.
# A write anywhere else fails this check.
#
# Usage: scripts/demo-smoke.sh [image] [container name]
set -euo pipefail

image="${1:-forgemap:ci}"
name="${2:-forgemap-smoke-$$}"

trap 'docker rm -f "$name" > /dev/null 2>&1 || true' EXIT
docker run -d --name "$name" --read-only --tmpfs /tmp --cap-drop ALL \
  --security-opt no-new-privileges "$image" -demo > /dev/null

for i in $(seq 1 30); do
  if docker exec "$name" wget -q -O /dev/null http://127.0.0.1:8080/ 2> /dev/null &&
    docker exec "$name" wget -q -O /dev/null http://127.0.0.1:8080/api/site.json 2> /dev/null; then
    echo "demo answered / and /api/site.json after ${i} s"
    exit 0
  fi
  sleep 1
done

echo "demo did not answer / and /api/site.json within 30 s; container logs:"
docker logs "$name" 2>&1 | tail -n 100
exit 1
