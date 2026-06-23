package managementapi

import (
	"net/http"
	"sync"
)

// /openapi.json + /docs (#94). The spec is generated once on first request and
// cached; Swagger UI is a tiny HTML shell that loads pinned CDN assets and
// points at /openapi.json. Both are registered in RegisterRoutes and exempted
// from the tenant-header middleware (see cmd/management-api/cors.go).

var (
	openapiOnce  sync.Once
	openapiSpec  []byte
	openapiError error
)

// ServeOpenAPISpec serves the generated OpenAPI 3.0 document.
func (h *Handler) ServeOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	openapiOnce.Do(func() { openapiSpec, openapiError = GenerateOpenAPI() })
	if openapiError != nil {
		http.Error(w, "failed to generate OpenAPI spec: "+openapiError.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write(openapiSpec)
}

// ServeSwaggerUI serves a minimal Swagger UI page bound to /openapi.json.
func (h *Handler) ServeSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerHTML))
}

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>VRSky Management API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"/>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({ url: '/openapi.json', dom_id: '#swagger-ui', deepLinking: true });
    };
  </script>
</body>
</html>`
