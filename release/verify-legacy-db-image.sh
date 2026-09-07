#!/usr/bin/env bash
# Verify a Navidrome image against a disposable copy of a legacy Redia database.
set -euo pipefail

db_dir=${1:?database directory required}
music_dir=${2:?music directory required}
image=${3:?image tag required}
name="navidrome-legacy-db-$PPID-$$"

cleanup() {
    docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --name "$name" --network none \
    --mount "type=bind,src=$db_dir,dst=/data" \
    --mount "type=bind,src=$music_dir,dst=/music" \
    -e ND_ENABLEINSIGHTSCOLLECTOR=false \
    -e ND_ENABLEEXTERNALSERVICES=false \
    -e ND_SCANNER_SCANONSTARTUP=false \
    -e ND_LOGLEVEL=info \
    "$image" >/dev/null

for _ in $(seq 1 45); do
    logs=$(docker logs "$name" 2>&1)
    if grep -q 'Navidrome server is ready' <<<"$logs"; then
        grep -E 'Version:|legacy STRM|20250823142158|successfully migrated|server is ready' <<<"$logs"
        exit 0
    fi
    if ! docker inspect "$name" --format '{{.State.Running}}' | grep -q true; then
        break
    fi
    sleep 1
done

docker logs "$name" >&2
echo 'FAIL: image did not start with the legacy database copy' >&2
exit 1
