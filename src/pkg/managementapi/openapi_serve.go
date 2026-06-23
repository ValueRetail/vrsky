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
//
// The Swagger assets come from a pinned CDN, hardened against supply-chain
// tampering two ways: Subresource Integrity (the browser refuses to run an
// asset whose hash doesn't match) and a Content-Security-Policy that confines
// script/style/font loads to that CDN and limits network calls to same origin.
func (h *Handler) ServeSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; "+
			"script-src https://cdn.jsdelivr.net 'unsafe-inline'; "+
			"style-src https://cdn.jsdelivr.net 'unsafe-inline'; "+
			"font-src https://cdn.jsdelivr.net; "+
			"img-src 'self' data:; "+
			"connect-src 'self'")
	_, _ = w.Write([]byte(swaggerHTML))
}

// Pinned to swagger-ui-dist@5.17.14 with SRI hashes; bump both the version and
// the integrity values together if upgrading.
const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>VRSky Management API</title>
  <link rel="stylesheet"
        href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"
        integrity="sha384-wxLW6kwyHktdDGr6Pv1zgm/VGJh99lfUbzSn6HNHBENZlCN7W602k9VkGdxuFvPn"
        crossorigin="anonymous"/>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"
          integrity="sha384-wmyclcVGX/WhUkdkATwhaK1X1JtiNrr2EoYJ+diV3vj4v6OC5yCeSu+yW13SYJep"
          crossorigin="anonymous"></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({ url: '/openapi.json', dom_id: '#swagger-ui', deepLinking: true });
    };
  </script>
</body>
</html>`
