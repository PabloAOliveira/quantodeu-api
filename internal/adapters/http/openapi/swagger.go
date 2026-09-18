package openapi

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Swagger UI 5.17.14 (swagger-ui-dist, licença Apache-2.0) embutido no
// binário: funciona offline, sem dependência de CDN e permite uma CSP
// restrita à própria origem.
//
//go:embed swaggerui/swagger-ui-bundle.js swaggerui/swagger-ui.css swaggerui/favicon-32x32.png swaggerui/LICENSE
var swaggerAssets embed.FS

const swaggerHTML = `<!doctype html>
<html lang="pt-BR">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>QuantoDeu API — Swagger</title>
  <link rel="icon" type="image/png" href="/docs/assets/favicon-32x32.png">
  <link rel="stylesheet" href="/docs/assets/swagger-ui.css">
  <link rel="stylesheet" href="/docs/assets/custom.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="/docs/assets/swagger-ui-bundle.js"></script>
  <script src="/docs/assets/init.js"></script>
</body>
</html>`

const swaggerInit = `window.addEventListener('load', function () {
  window.ui = SwaggerUIBundle({
    url: '/openapi.json',
    dom_id: '#swagger-ui',
    deepLinking: true,
    displayRequestDuration: true,
    persistAuthorization: true,
    tryItOutEnabled: true,
    filter: true,
    docExpansion: 'list',
    defaultModelsExpandDepth: 0,
    // Mesma origem: o navegador envia o cookie HttpOnly de sessão sozinho.
    requestInterceptor: function (req) { req.credentials = 'same-origin'; return req; }
  });
});`

const swaggerCSS = `.swagger-ui .topbar { display: none; }
body { margin: 0; background: #fafafa; }`

// RegisterUI expõe /openapi.json e o Swagger UI em /docs.
//
// A página usa CSP própria (a API usa default-src 'none'): tudo só da
// própria origem; 'unsafe-inline' apenas em estilos (o Swagger UI injeta CSS).
func (d *Doc) RegisterUI(r gin.IRoutes) {
	const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

	asset := func(name, contentType string) gin.HandlerFunc {
		b, err := swaggerAssets.ReadFile("swaggerui/" + name)
		if err != nil {
			panic(err) // arquivo embutido: falha só se o build estiver quebrado
		}
		return func(c *gin.Context) {
			c.Header("Cache-Control", "public, max-age=86400")
			c.Data(http.StatusOK, contentType, b)
		}
	}

	r.GET("/openapi.json", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "application/json; charset=utf-8", d.Spec())
	})
	r.GET("/docs", func(c *gin.Context) {
		c.Header("Content-Security-Policy", csp)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerHTML))
	})
	r.GET("/docs/assets/swagger-ui-bundle.js", asset("swagger-ui-bundle.js", "application/javascript; charset=utf-8"))
	r.GET("/docs/assets/swagger-ui.css", asset("swagger-ui.css", "text/css; charset=utf-8"))
	r.GET("/docs/assets/favicon-32x32.png", asset("favicon-32x32.png", "image/png"))
	r.GET("/docs/assets/init.js", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(swaggerInit))
	})
	r.GET("/docs/assets/custom.css", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/css; charset=utf-8", []byte(swaggerCSS))
	})
}
