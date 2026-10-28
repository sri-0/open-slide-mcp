BIN      := bin/open-slide-mcp
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE    ?= open-slide-mcp:local
PORT     ?= 8090

.PHONY: setup shell sidecar build run test lint docker-build docker-run docker-stop smoke isolation opencode-config clean

## setup: install the shell and sidecar dependencies (needs Node 22+ and pnpm)
setup:
	cd shell && pnpm install --frozen-lockfile
	cd sidecar && pnpm install --frozen-lockfile

## shell: build the open-slide editor once into shell/dist
shell:
	cd shell && pnpm build

## sidecar: fetch open-slide's editing sources at the shell's pinned version and bundle sidecar/dist/server.js
sidecar:
	cd sidecar && pnpm upstream && pnpm build

## build: compile the wrapper binary
build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/open-slide-mcp

## run: run the wrapper locally; decks are served on http://<deck>.decks.localhost:$(PORT)
run: build
	WORKSPACE_DIR=./workspace SHELL_DIR=./shell/dist MODULES_DIR=./shell/node_modules SIDECAR_DIR=./sidecar/dist \
	DECK_ORIGIN=$${DECK_ORIGIN:-http://{deck}.decks.localhost:$(PORT)} EMBED_ORIGINS=$${EMBED_ORIGINS:-http://localhost:3000} \
	LISTEN_ADDR=:$(PORT) LOG_LEVEL=$${LOG_LEVEL:-info} $(BIN)

## test: unit tests
test:
	go test ./...

## lint: vet
lint:
	go vet ./...

## docker-build: build the container image
docker-build:
	docker build -t $(IMAGE) .

## docker-run: run the container with persistent slides and metadata volumes
docker-run:
	docker run --rm -d --name open-slide-mcp -p $(PORT):8090 \
		-e DECK_ORIGIN=http://{deck}.decks.localhost:$(PORT) \
		-e EMBED_ORIGINS=$${EMBED_ORIGINS:-http://localhost:3000} \
		-v open-slide-mcp-slides:/app/workspace/slides \
		-v open-slide-mcp-meta:/app/workspace/.open-slide-mcp \
		$(IMAGE)
	@echo "container started; MCP at http://localhost:$(PORT)/mcp"

docker-stop:
	docker stop open-slide-mcp

## smoke: drive every tool through the MCP endpoint and open the preview
smoke:
	go run ./cmd/smoke -url http://localhost:$(PORT)/mcp

## isolation: curl the deck origins to prove tokens, cookies and cross-deck refusals
isolation:
	scripts/isolation.sh http://localhost:$(PORT)

## opencode-config: print the block to add to ~/.config/opencode/opencode.json
opencode-config:
	@printf '  "open-slide": {\n    "type": "remote",\n    "url": "http://localhost:$(PORT)/mcp",\n    "enabled": true\n  }\n'

clean:
	rm -rf bin shell/dist sidecar/dist sidecar/upstream
