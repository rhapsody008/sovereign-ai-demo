VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/rhapsody008/sovereign-ai-demo
COMPOSE := docker compose -f deploy/local/docker-compose.yaml

.PHONY: run mock test lint docker-build up down logs seed

run: ## run the app locally (in-memory store unless S3_* is set)
	go run -ldflags "-X main.version=$(VERSION)" ./cmd/server

mock: ## run the mock NAI endpoint on :9090
	go run ./cmd/mocknai

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

docker-build:
	docker build --target app --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .
	docker build --target mocknai -t $(IMAGE)-mocknai:$(VERSION) .

up:
	$(COMPOSE) up -d --build
	@echo "primary: http://localhost:8080/piishield/  standby: http://localhost:8081/piishield/  s3: http://localhost:8333"

down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f app app-standby

seed: ## file a demo record through the API (uses the configured NAI)
	curl -s -XPOST http://localhost:8080/piishield/api/intake -F 'text=This is Zhou Yi with email yi.zhou@nutanix.com. File this.'; echo
