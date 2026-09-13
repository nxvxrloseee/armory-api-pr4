// Package openapi serves the hand-written OpenAPI 3.0 spec
// (openapi.yaml) and a Swagger UI page that renders it — both embedded
// into the binary so `go run .` alone is enough, no separate asset step.
package openapi

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var spec []byte

const docsPage = `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <title>armory_api — документация</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css" />
  <style>body { margin: 0; }</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: '/api/openapi.yaml',
      dom_id: '#swagger-ui',
      presets: [SwaggerUIBundle.presets.apis],
    });
  </script>
</body>
</html>`

// SpecHandler serves the raw spec at /api/openapi.yaml — which
// armory_web's report can also point to as "описание используемых
// эндпоинтов" — and DocsHandler serves the Swagger UI page that reads it.
func SpecHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = w.Write(spec)
}

func DocsHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}
