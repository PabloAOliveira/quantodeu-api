// Package openapi gera a especificação OpenAPI 3.1 a partir das PRÓPRIAS
// rotas e DTOs registrados no Gin (reflection sobre as structs e tags).
//
// Há uma única fonte da verdade: a rota é registrada com Register, que ao
// mesmo tempo liga o handler no Gin e documenta método, caminho, query, corpo,
// respostas e segurança. Não existe arquivo YAML mantido à mão que possa
// divergir do código.
//
// Tags suportadas nos DTOs:
//
//	json:"nome,omitempty"          nome do campo / opcional
//	binding:"required,max=60,min=1,oneof=a b"   obrigatoriedade e limites
//	form:"mes"                     parâmetro de query
//	doc:"descrição"                descrição do campo
//	example:"150,00"               exemplo exibido no Swagger
//
// Tipos podem customizar o próprio schema implementando SchemaProvider.
package openapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// SchemaProvider permite que um tipo defina seu próprio JSON Schema.
type SchemaProvider interface {
	OpenAPISchema() map[string]any
}

// Security define o esquema de autenticação de uma operação.
type Security int

const (
	// NoAuth não exige autenticação.
	NoAuth Security = iota
	// CookieAuth exige o cookie de sessão.
	CookieAuth
	// WebhookAuth exige o token do webhook.
	WebhookAuth
)

// Response documenta uma resposta.
type Response struct {
	Description string
	Body        any // instância zero do DTO (nil = sem corpo)
}

// Route descreve uma operação.
type Route struct {
	Method      string
	Path        string // sintaxe Gin: /transacoes/:id
	OperationID string
	Summary     string
	Description string
	Tags        []string
	Security    Security
	Query       any // struct com tags form
	Body        any
	Responses   map[int]Response
	// Errors lista status de erro adicionais (401/422/429/500 são inferidos).
	Errors []int
}

// Info são os metadados da API.
type Info struct {
	Title       string
	Version     string
	Description string
	CookieName  string
}

// Doc acumula as rotas e gera a especificação.
type Doc struct {
	info      Info
	errorBody any
	mu        sync.Mutex
	routes    []Route
	schemas   map[string]any
	cached    []byte
}

// New cria o documento. errorBody é o DTO padrão de erro.
func New(info Info, errorBody any) *Doc {
	return &Doc{info: info, errorBody: errorBody, schemas: map[string]any{}}
}

// Register liga os handlers no Gin E documenta a rota.
func (d *Doc) Register(g gin.IRoutes, base string, r Route, handlers ...gin.HandlerFunc) {
	g.Handle(r.Method, r.Path, handlers...)
	r.Path = strings.TrimRight(base, "/") + r.Path
	d.mu.Lock()
	d.routes = append(d.routes, r)
	d.cached = nil
	d.mu.Unlock()
}

var rePathParam = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

// Spec monta a especificação OpenAPI 3.1 (em cache após a primeira geração).
func (d *Doc) Spec() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached != nil {
		return d.cached
	}

	paths := map[string]map[string]any{}
	for _, r := range d.routes {
		p := rePathParam.ReplaceAllString(r.Path, "{$1}")
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		paths[p][strings.ToLower(r.Method)] = d.operation(r)
	}

	spec := map[string]any{
		"openapi":           "3.1.0",
		"jsonSchemaDialect": "https://spec.openapis.org/oas/3.1/dialect/base",
		"info": map[string]any{
			"title": d.info.Title, "version": d.info.Version, "description": d.info.Description,
		},
		"servers": []any{map[string]any{"url": "/", "description": "Este servidor"}},
		"paths":   paths,
		"components": map[string]any{
			"schemas": d.schemas,
			"securitySchemes": map[string]any{
				"cookieAuth": map[string]any{
					"type": "apiKey", "in": "cookie", "name": d.info.CookieName,
					"description": "Cookie HttpOnly emitido por POST /api/v1/auth/login. No Swagger UI, faça login primeiro: o navegador envia o cookie automaticamente.",
				},
				"bearerAuth": map[string]any{
					"type": "http", "scheme": "bearer",
					"description": "Apps nativos: faça POST /api/v1/auth/login com \"modo\":\"token\" e envie o token em Authorization: Bearer <token>.",
				},
				"webhookToken": map[string]any{
					"type": "apiKey", "in": "header", "name": "X-Webhook-Token",
					"description": "Segredo WHATSAPP_WEBHOOK_SECRET (Evolution/Z-API). Twilio usa X-Twilio-Signature.",
				},
			},
		},
		"tags": d.tags(),
	}
	b, _ := json.MarshalIndent(spec, "", "  ")
	d.cached = b
	return b
}

func (d *Doc) tags() []any {
	seen := map[string]bool{}
	var out []any
	for _, r := range d.routes {
		for _, t := range r.Tags {
			if !seen[t] {
				seen[t] = true
				out = append(out, map[string]any{"name": t})
			}
		}
	}
	return out
}

func (d *Doc) operation(r Route) map[string]any {
	op := map[string]any{
		"operationId": r.OperationID,
		"summary":     r.Summary,
		"tags":        r.Tags,
	}
	if r.Description != "" {
		op["description"] = r.Description
	}

	var params []any
	for _, m := range rePathParam.FindAllStringSubmatch(r.Path, -1) {
		params = append(params, map[string]any{
			"name": m[1], "in": "path", "required": true,
			"schema": map[string]any{"type": "string", "format": "uuid"},
		})
	}
	if r.Query != nil {
		params = append(params, d.queryParams(reflect.TypeOf(r.Query))...)
	}
	if len(params) > 0 {
		op["parameters"] = params
	}

	if r.Body != nil {
		op["requestBody"] = map[string]any{
			"required": true,
			"content":  map[string]any{"application/json": map[string]any{"schema": d.schemaFor(reflect.TypeOf(r.Body))}},
		}
	}

	responses := map[string]any{}
	for status, resp := range r.Responses {
		entry := map[string]any{"description": resp.Description}
		if resp.Body != nil {
			entry["content"] = map[string]any{"application/json": map[string]any{"schema": d.schemaFor(reflect.TypeOf(resp.Body))}}
		}
		responses[strconv.Itoa(status)] = entry
	}
	errs := append([]int{}, r.Errors...)
	switch r.Security {
	case CookieAuth:
		op["security"] = []any{map[string]any{"cookieAuth": []any{}}, map[string]any{"bearerAuth": []any{}}}
		errs = append(errs, http.StatusUnauthorized)
	case WebhookAuth:
		op["security"] = []any{map[string]any{"webhookToken": []any{}}}
		errs = append(errs, http.StatusUnauthorized)
	default:
		op["security"] = []any{}
	}
	if r.Body != nil || r.Query != nil {
		errs = append(errs, http.StatusUnprocessableEntity)
	}
	errs = append(errs, http.StatusTooManyRequests, http.StatusInternalServerError)
	errSchema := d.schemaFor(reflect.TypeOf(d.errorBody))
	for _, s := range errs {
		key := strconv.Itoa(s)
		if _, ok := responses[key]; ok {
			continue
		}
		responses[key] = map[string]any{
			"description": http.StatusText(s),
			"content":     map[string]any{"application/json": map[string]any{"schema": errSchema}},
		}
	}
	op["responses"] = responses
	return op
}

func (d *Doc) queryParams(t reflect.Type) []any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []any
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("form"), ",")[0]
		if name == "" || name == "-" || !f.IsExported() {
			continue
		}
		schema := d.fieldSchema(f)
		p := map[string]any{"name": name, "in": "query", "required": hasRule(f, "required"), "schema": schema}
		if desc := f.Tag.Get("doc"); desc != "" {
			p["description"] = desc
		}
		out = append(out, p)
	}
	return out
}

var (
	timeType     = reflect.TypeOf(time.Time{})
	providerType = reflect.TypeOf((*SchemaProvider)(nil)).Elem()
)

// schemaFor devolve o schema (ou $ref para structs nomeadas).
func (d *Doc) schemaFor(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(providerType) {
		return reflect.Zero(t).Interface().(SchemaProvider).OpenAPISchema()
	}
	if reflect.PointerTo(t).Implements(providerType) {
		return reflect.New(t).Interface().(SchemaProvider).OpenAPISchema()
	}
	switch t {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return map[string]any{"type": "integer", "format": "int32"}
	case reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": d.schemaFor(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": d.schemaFor(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		if t.Name() == "" {
			return d.structSchema(t)
		}
		name := t.Name()
		if _, ok := d.schemas[name]; !ok {
			d.schemas[name] = map[string]any{} // evita recursão infinita
			d.schemas[name] = d.structSchema(t)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	}
	return map[string]any{}
}

func (d *Doc) structSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	d.collectFields(t, props, &required)
	sort.Strings(required)
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (d *Doc) collectFields(t reflect.Type, props map[string]any, required *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		jsonTag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(jsonTag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				d.collectFields(ft, props, required)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		props[name] = d.fieldSchema(f)
		isReq := hasRule(f, "required") ||
			(f.Tag.Get("binding") == "" && !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Pointer && f.Tag.Get("form") == "")
		if isReq {
			*required = append(*required, name)
		}
	}
}

func (d *Doc) fieldSchema(f reflect.StructField) map[string]any {
	base := d.schemaFor(f.Type)
	s := make(map[string]any, len(base)+4)
	for k, v := range base {
		s[k] = v
	}
	if _, isRef := s["$ref"]; isRef {
		// Em 3.1 é permitido irmãos de $ref; mantemos só a descrição.
		if desc := f.Tag.Get("doc"); desc != "" {
			s["description"] = desc
		}
		return s
	}
	if desc := f.Tag.Get("doc"); desc != "" {
		s["description"] = desc
	}
	if ex, ok := f.Tag.Lookup("example"); ok {
		s["examples"] = []any{exampleValue(s, ex)}
	}
	typ, _ := s["type"].(string)
	for _, rule := range strings.Split(f.Tag.Get("binding"), ",") {
		key, val, _ := strings.Cut(rule, "=")
		switch key {
		case "max":
			n, _ := strconv.ParseFloat(val, 64)
			if typ == "string" {
				s["maxLength"] = int(n)
			} else if typ == "integer" || typ == "number" {
				s["maximum"] = n
			}
		case "min":
			n, _ := strconv.ParseFloat(val, 64)
			if typ == "string" {
				s["minLength"] = int(n)
			} else if typ == "integer" || typ == "number" {
				s["minimum"] = n
			}
		case "oneof":
			var enum []any
			for _, v := range strings.Fields(val) {
				enum = append(enum, exampleValue(s, v))
			}
			s["enum"] = enum
		case "len":
			n, _ := strconv.Atoi(val)
			s["minLength"], s["maxLength"] = n, n
		case "numeric":
			s["pattern"] = `^[0-9]+$`
		}
	}
	return s
}

func hasRule(f reflect.StructField, rule string) bool {
	for _, r := range strings.Split(f.Tag.Get("binding"), ",") {
		if r == rule {
			return true
		}
	}
	return false
}

func exampleValue(schema map[string]any, raw string) any {
	switch schema["type"] {
	case "integer":
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n
		}
	case "number":
		if n, err := strconv.ParseFloat(raw, 64); err == nil {
			return n
		}
	case "boolean":
		if b, err := strconv.ParseBool(raw); err == nil {
			return b
		}
	}
	return raw
}
