# Build the Go wrapper.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /out/open-slide-mcp ./cmd/open-slide-mcp

# Runtime: node for the open-slide dev server, plus the wrapper binary.
FROM node:22-bookworm-slim
RUN corepack enable && corepack prepare pnpm@9.7.1 --activate
WORKDIR /app/workspace
COPY workspace/package.json workspace/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY workspace/ ./
COPY --from=build /out/open-slide-mcp /usr/local/bin/open-slide-mcp
RUN mkdir -p slides .open-slide-mcp && chown -R node:node /app
USER node
ENV WORKSPACE_DIR=/app/workspace \
    LISTEN_ADDR=:8090 \
    VITE_HOST=127.0.0.1 \
    VITE_PORT=5173 \
    PUBLIC_URL=http://localhost:8090 \
    PREVIEW_AUTH=false
EXPOSE 8090
VOLUME ["/app/workspace/slides", "/app/workspace/.open-slide-mcp"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=30s CMD node -e "fetch('http://127.0.0.1:8090/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"
ENTRYPOINT ["open-slide-mcp"]
