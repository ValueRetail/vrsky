# API reference

The VRSky Management API is described by an **OpenAPI 3.0** spec generated from
the live route registry. It is always in sync with the running server — CI fails
the build if a route is added without a spec entry.

- **Spec:** `GET /openapi.json` (served by the management API)
- **Interactive docs (Swagger UI):** `GET /docs`

Locally that's <http://localhost:3000/openapi.json> and
<http://localhost:3000/docs>.

## Authentication

Most endpoints require two things:

1. A **session cookie** (`vrsky_session`), set by `POST /api/v1/auth/login`.
2. An **`X-Tenant-ID`** header selecting the active workspace.

Exceptions: the `/api/v1/auth/*` routes (no tenant), the OAuth callback, the
Alertmanager webhook, and the infra endpoints (`/healthz`, `/readyz`,
`/metrics`, `/openapi.json`, `/docs`). The tenant data-ingestion endpoint uses a
per-tenant **API key** instead of a session.

## Browse it live

<div id="swagger-ui"></div>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"/>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js" crossorigin></script>
<script>
  window.addEventListener('load', function () {
    if (window.SwaggerUIBundle) {
      window.SwaggerUIBundle({
        url: (location.origin.indexOf('localhost') >= 0 ? 'http://localhost:3000' : '') + '/openapi.json',
        dom_id: '#swagger-ui',
      });
    }
  });
</script>

!!! note
    The embedded explorer above loads `/openapi.json` from the API origin. When
    viewing the static docs site, point it at your deployment's API host, or
    just open `/docs` on the management API directly.
