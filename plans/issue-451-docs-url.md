# Issue 451: Help & Docs link 404

## Problem

The sidebar's Help & Docs link opens `config.docsUrl`, default
`https://valueretail.github.io/vrsky/`. It returned 404 because GitHub Pages was
not enabled. Pages is now on, serving the `gh-pages` branch (done by a human on
2026-10-29). Nothing would have told us if it broke again.

## Findings

- No `CNAME` on `gh-pages`, so the URL is the default project-site URL
  `https://<owner>.github.io/<repo>/`, i.e. `https://valueretail.github.io/vrsky/`.
  The sandbox proxy blocks that host, so the 200 is checked by the new CI job,
  not by hand.
- `mkdocs.yml` had no `site_url`.
- The UI default already matched. No `VITE_DOCS_URL` is set in `ui/Dockerfile`,
  `build-push.yml` or `infrastructure/`, so production uses the default.

## Acceptance criteria

1. Help & Docs opens the docs site in production: UI default, `mkdocs.yml`
   `site_url` and the published URL are the same string.
2. A check fails if the docs URL stops answering 200.

## Changes

- `mkdocs.yml`: add `site_url`.
- `scripts/check-docs-url.sh`: offline check that `site_url` equals the UI
  default; online check that the URL answers 200 (redirects followed, retries
  for Pages propagation).
- `scripts/check-docs-url_test.sh`: tests the script against a local stub
  (200, 404, connection refused) and the repo's own consistency.
- `ui/src/config/env.test.ts`: the default `docsUrl` equals `site_url`.
- `.github/workflows/docs.yml` (needs a human): new `published` job after
  deploy plus a daily schedule and manual dispatch; PR job runs the offline
  check and the script test; PR paths widened to the script and `env.ts`.
  `ci-failure-alert.yml` already watches `docs`, so no change there.
- `AGENTS.md`: docs site section names the URL and the check.

## Touches

`mkdocs.yml`, `scripts/`, `ui/src/config/`, `.github/workflows/docs.yml`,
`AGENTS.md`, `plans/`.
