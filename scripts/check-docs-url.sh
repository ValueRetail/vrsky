#!/usr/bin/env bash
# Fails when the published docs site is not reachable (#451).
#
# The sidebar's "Help & Docs" link opens the mkdocs site on GitHub Pages. It
# returned 404 for weeks because Pages was never switched on, and nothing
# noticed. This script is that notice.
#
# It checks two things:
#   1. The URL is the same in mkdocs.yml (site_url) and in the UI default
#      (docsUrl in ui/src/config/env.ts). Offline, so it also runs on PRs.
#   2. The URL answers 200 (redirects followed), retrying for a while because
#      Pages takes a minute or two to publish after a deploy.
#
# Usage: scripts/check-docs-url.sh [--offline]
#   --offline   only the consistency check, no network.
# Environment:
#   DOCS_URL_ATTEMPTS  tries before giving up (default 12)
#   DOCS_URL_DELAY     seconds between tries (default 15)
#   DOCS_URL_OVERRIDE  fetch this URL instead of site_url (used by the test)

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
offline=false
[ "${1:-}" = "--offline" ] && offline=true

site_url="$(sed -n 's/^site_url:[[:space:]]*//p' "$root/mkdocs.yml" | tr -d '"'"'"' \r')"
ui_url="$(sed -n "s/.*getEnv('VITE_DOCS_URL', *'\([^']*\)').*/\1/p" "$root/ui/src/config/env.ts")"

if [ -z "$site_url" ]; then
  echo "FAIL: mkdocs.yml has no site_url" >&2
  exit 1
fi
if [ -z "$ui_url" ]; then
  echo "FAIL: could not find the VITE_DOCS_URL default in ui/src/config/env.ts" >&2
  exit 1
fi
if [ "$site_url" != "$ui_url" ]; then
  echo "FAIL: mkdocs.yml site_url ($site_url) differs from the UI default docsUrl ($ui_url)" >&2
  exit 1
fi
echo "ok: site_url and the UI default agree: $site_url"

if $offline; then
  exit 0
fi

url="${DOCS_URL_OVERRIDE:-$site_url}"
attempts="${DOCS_URL_ATTEMPTS:-12}"
delay="${DOCS_URL_DELAY:-15}"
code=000
for i in $(seq 1 "$attempts"); do
  code="$(curl -sS -L -o /dev/null -w '%{http_code}' --max-time 20 "$url" || true)"
  [ "$code" = "200" ] && { echo "ok: $url answered 200"; exit 0; }
  echo "attempt $i/$attempts: $url answered $code"
  [ "$i" -lt "$attempts" ] && sleep "$delay"
done
echo "FAIL: $url did not answer 200 (last status $code). Is GitHub Pages enabled on the gh-pages branch?" >&2
exit 1
