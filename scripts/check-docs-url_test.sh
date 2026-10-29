#!/usr/bin/env bash
# Tests scripts/check-docs-url.sh against a local stub server. No network.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"
pid=""
trap '[ -n "$pid" ] && kill "$pid" 2>/dev/null; rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# Consistency check passes on the repo as it is.
"$here/check-docs-url.sh" --offline >/dev/null || fail "offline check failed on the repo"

# Stub: / answers 200, /missing answers 404.
mkdir -p "$tmp/www"; echo ok > "$tmp/www/index.html"
python3 -I -u -m http.server 0 --bind 127.0.0.1 --directory "$tmp/www" >"$tmp/log" 2>&1 &
pid=$!
port=""
for _ in $(seq 1 50); do
  port="$(sed -n 's/.*port \([0-9]*\).*/\1/p' "$tmp/log" | head -1)"
  [ -n "$port" ] && break
  sleep 0.1
done
[ -n "$port" ] || fail "stub server did not start"

export DOCS_URL_ATTEMPTS=2 DOCS_URL_DELAY=0
DOCS_URL_OVERRIDE="http://127.0.0.1:$port/" "$here/check-docs-url.sh" >/dev/null \
  || fail "200 should pass"
if DOCS_URL_OVERRIDE="http://127.0.0.1:$port/missing" "$here/check-docs-url.sh" >/dev/null 2>&1; then
  fail "404 should fail"
fi
if DOCS_URL_OVERRIDE="http://127.0.0.1:1/" "$here/check-docs-url.sh" >/dev/null 2>&1; then
  fail "connection refused should fail"
fi
echo "all check-docs-url tests passed"
