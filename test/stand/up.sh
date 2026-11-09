#!/bin/sh
# Starts the stand: debian on localhost:2201, alpine on localhost:2202.
# Users: bashuser, tcshuser, fishuser; remote_root is /home/<user>/proj (has the marker)
# or /home/<user>/nomarker (no marker). Key: key/id_ed25519 (not committed to git).
# Stop: docker compose -f test/stand/compose.yaml down
set -e
cd "$(dirname "$0")"
mkdir -p key
[ -f key/id_ed25519 ] || ssh-keygen -q -t ed25519 -N '' -C owysync-stand -f key/id_ed25519
docker compose up -d --build
