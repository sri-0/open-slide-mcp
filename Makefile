BIN      := bin/open-slide-mcp
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE    ?= open-slide-mcp:local
PORT     ?= 8090

.PHONY: setup build run test lint docker-build docker-run docker-stop smoke opencode-config clean

## setup: install the open-slide workspace dependencies (needed for local runs)
setup:
	cd workspace && pnpm install --frozen-lockfile

## build: compile the wrapper binary
build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/open-slide-mcp

## run: run the wrapper locally against ./workspace (no container)
run: build
	WORKSPACE_DIR=./workspace LISTEN_ADDR=:$(PORT) PUBLIC_URL=http://localhost:$(PORT) LOG_LEVEL=$${LOG_LEVEL:-info} $(BIN)

## test: unit tests
test:
	go test ./...

## lint: vet
lint:
	go vet ./...

## docker-build: build the container image
docker-build:
	docker build -t $(IMAGE) .

## docker-run: run the container with a persistent slides volume
docker-run:
	docker run --rm -d --name open-slide-mcp -p $(PORT):8090 \
		-e PUBLIC_URL=http://localhost:$(PORT) \
		-v open-slide-mcp-slides:/app/workspace/slides \
		-v open-slide-mcp-meta:/app/workspace/.open-slide-mcp \
		$(IMAGE)
	@echo "container started; MCP at http://localhost:$(PORT)/mcp"

docker-stop:
	docker stop open-slide-mcp

## smoke: exercise the MCP endpoint end to end against a running server
smoke:
	go run ./cmd/smoke -url http://localhost:$(PORT)/mcp

## opencode-config: print the block to add to ~/.config/opencode/opencode.json
opencode-config:
	@printf '  "open-slide": {\n    "type": "remote",\n    "url": "http://localhost:$(PORT)/mcp",\n    "enabled": true\n  }\n'

clean:
	rm -rf bin
