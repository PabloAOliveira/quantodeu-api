package openapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type money struct{}

func (money) OpenAPISchema() map[string]any {
	return map[string]any{"type": "string", "format": "money"}
}

type item struct {
	Nome  string `json:"nome" binding:"required,max=60" example:"mercado" doc:"Nome"`
	Tipo  string `json:"tipo" binding:"omitempty,oneof=entrada saida"`
	Valor money  `json:"valor"`
	Qtd   int    `json:"qtd,omitempty" binding:"min=1,max=10"`
}

type query struct {
	Page int  `form:"page" binding:"omitempty,min=1"`
	All  bool `form:"all"`
}

type resp struct {
	Items []item  `json:"items"`
	Note  *string `json:"note,omitempty"`
}

type errBody struct {
	Code string `json:"code"`
}

func TestSpecGeradaDosDTOs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	doc := New(Info{Title: "T", Version: "1", CookieName: "sid"}, errBody{})
	g := r.Group("/api")
	doc.Register(g, "/api", Route{Method: http.MethodPut, Path: "/itens/:id", OperationID: "put", Tags: []string{"X"},
		Security: CookieAuth, Query: query{}, Body: item{},
		Responses: map[int]Response{200: {Description: "ok", Body: resp{}}}}, func(c *gin.Context) { c.Status(200) })
	doc.RegisterUI(r)

	// A rota foi de fato registrada no Gin.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/itens/1", nil))
	if w.Code != 200 {
		t.Fatalf("rota não registrada: %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	var spec map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Fatal("versão incorreta")
	}
	op := spec["paths"].(map[string]any)["/api/itens/{id}"].(map[string]any)["put"].(map[string]any)
	params := op["parameters"].([]any)
	if len(params) != 3 || params[0].(map[string]any)["in"] != "path" {
		t.Fatalf("parâmetros = %v", params)
	}
	resps := op["responses"].(map[string]any)
	for _, code := range []string{"200", "401", "422", "429", "500"} {
		if _, ok := resps[code]; !ok {
			t.Errorf("resposta %s ausente", code)
		}
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	it := schemas["item"].(map[string]any)
	props := it["properties"].(map[string]any)
	if props["nome"].(map[string]any)["maxLength"] != float64(60) {
		t.Errorf("maxLength não gerado: %v", props["nome"])
	}
	if enum := props["tipo"].(map[string]any)["enum"].([]any); len(enum) != 2 {
		t.Errorf("enum não gerado: %v", props["tipo"])
	}
	if props["valor"].(map[string]any)["format"] != "money" {
		t.Errorf("SchemaProvider ignorado: %v", props["valor"])
	}
	req := it["required"].([]any)
	if len(req) != 2 || req[0] != "nome" || req[1] != "valor" {
		t.Errorf("required = %v", req)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("/docs = %d", w.Code)
	}
}
