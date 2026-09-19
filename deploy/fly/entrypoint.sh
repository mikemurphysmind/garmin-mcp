#!/bin/sh
set -eu
umask 077

fail() { echo "fly-entrypoint: $*" >&2; exit 1; }
[ ! -L /data ] && mountpoint -q /data || fail "/data must be a mounted volume"

# Do not recursively chown/chmod existing state or repair questionable key files.
if [ ! -e /data/garmin ] && [ ! -L /data/garmin ]; then
    mkdir -m 700 /data/garmin
    chown 65532:65532 /data/garmin
fi
[ ! -L /data/garmin ] && [ -d /data/garmin ] || fail "invalid state directory"
[ "$(stat -c '%u:%g:%a' /data/garmin)" = "65532:65532:700" ] || \
    fail "state directory must be owned by 65532:65532 with mode 700"

# Require an explicit account allowlist for this personal deployment.
[ -n "${GARMIN_MCP_LOGIN_ALLOWED_EMAILS:-}" ] || fail "configure the login allowlist first"
[ -n "${GARMIN_MCP_OAUTH_CLIENTS:-}" ] || fail "configure an OAuth client first"
exec su-exec 65532:65532 /usr/local/bin/garmin-mcp "$@"
