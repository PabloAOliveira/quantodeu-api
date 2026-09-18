.PHONY: help tidy run build test test-race test-integration smoke mail mail-test check-config vet fmt lint docker-up docker-up-full docker-down docker-logs secret psql

help: ## Lista os comandos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

tidy: ## Baixa dependências e gera go.sum
	go mod tidy

run: ## Executa localmente (lê .env)
	go run ./cmd/api

build: ## Compila o binário em ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/quantodeu-api ./cmd/api

test: ## Testes unitários
	go test ./...

test-race: ## Testes unitários com race detector
	go test -race ./...

# Requer Postgres (dono + usuário da API) e Redis. Com o compose no ar:
#   make test-integration
test-integration: ## Testes de integração (RLS, repositórios, Redis)
	TEST_DATABASE_URL=$${TEST_DATABASE_URL:-postgres://quantodeu:quantodeu@localhost:5432/quantodeu?sslmode=disable} \
	TEST_APP_DATABASE_URL=$${TEST_APP_DATABASE_URL:-postgres://quantodeu_api:troque-app-db@localhost:5432/quantodeu?sslmode=disable} \
	TEST_REDIS_ADDR=$${TEST_REDIS_ADDR:-localhost:6379} \
	go test -tags=integration -count=1 ./internal/adapters/db/...

smoke: ## Smoke test ponta a ponta contra a API rodando
	API_URL=$${API_URL:-http://localhost:8080} WEBHOOK_SECRET=$${WEBHOOK_SECRET:-$$(grep -E '^WHATSAPP_WEBHOOK_SECRET=' .env | cut -d= -f2 | cut -d' ' -f1)} bash ./scripts/smoke-test.sh

check-config: ## Valida a config e aponta riscos, sem subir a API nem enviar nada
	go run ./cmd/api -check-config

mail-test: ## Envia um e-mail de teste com a config atual (make mail-test TO=voce@exemplo.com)
	@test -n "$(TO)" || { echo "uso: make mail-test TO=voce@exemplo.com"; exit 1; }
	go run ./cmd/api -test-email=$(TO)

mail: ## Abre a caixa de e-mails de teste (Mailpit)
	@open http://localhost:8025 2>/dev/null || echo "Abra http://localhost:8025"

vet: ## go vet (inclui arquivos de integração)
	go vet ./... && go vet -tags=integration ./...

fmt: ## gofmt
	gofmt -w .

lint: ## golangci-lint (se instalado)
	golangci-lint run ./...

docker-up: ## Sobe API + Postgres
	docker compose up --build -d

docker-up-full: ## Sobe API + Postgres + Redis + Prometheus + Jaeger
	docker compose --profile redis --profile observability up --build -d

docker-down: ## Derruba os containers (mantém volumes)
	docker compose --profile redis --profile observability down

docker-logs: ## Acompanha os logs da API
	docker compose logs -f api

psql: ## Abre o psql como dono das tabelas
	docker compose exec db psql -U $${POSTGRES_USER:-quantodeu} -d $${POSTGRES_DB:-quantodeu}

secret: ## Gera um segredo aleatório
	@openssl rand -base64 48
