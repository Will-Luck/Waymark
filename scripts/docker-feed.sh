#!/usr/bin/env bash
# Writes the Docker feed Waymark reads (docker.feed): one JSON object per
# container from `docker ps -a`. Run it from the host's cron every minute,
# e.g. with the script installed in /usr/local/bin:
#
#   * * * * * FEED_DIR=/var/lib/waymark-feed /usr/local/bin/docker-feed.sh
#
# Mount the DIRECTORY read-only in the container (-v /var/lib/waymark-feed:/feed:ro,
# docker.feed: /feed/docker.json), never the single file: the file is replaced
# by a rename, which detaches a single-file bind mount. Because it is written
# to a temp name and renamed, a reader never sees half a feed. If docker fails
# or hangs, the old feed stays in place and Waymark labels it stale.
set -euo pipefail

FEED_DIR="${FEED_DIR:-/var/lib/waymark-feed}"
FEED_TIMEOUT="${FEED_TIMEOUT:-30}" # seconds docker ps may take
mkdir -p "$FEED_DIR"

# One run at a time: while docker is wedged, later cron runs exit at once
# instead of piling up.
exec 9>"$FEED_DIR/.lock"
flock -n 9 || exit 0

tmp=$(mktemp "$FEED_DIR/.docker.json.XXXXXX")
trap 'rm -f "$tmp"' EXIT
timeout "$FEED_TIMEOUT" docker ps -a --format '{{json .}}' > "$tmp"
chmod 644 "$tmp"
mv -f "$tmp" "$FEED_DIR/docker.json"
