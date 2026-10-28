# The editor shell: open-slide's app built once into static files, plus the
# pinned copy of lucide-react the compiler bundles into decks.
FROM node:22-bookworm-slim AS shell
RUN corepack enable && corepack prepare pnpm@9.7.1 --activate
WORKDIR /src/shell
COPY shell/package.json shell/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY shell/ ./
RUN pnpm build && mkdir -p /out/modules && cp -rL node_modules/lucide-react /out/modules/lucide-react

# The parse-only editing sidecar, bundled to one file. Its open-slide sources
# are fetched from the repository tag matching the shell's pinned version.
FROM node:22-bookworm-slim AS sidecar
RUN corepack enable && corepack prepare pnpm@9.7.1 --activate
WORKDIR /src/sidecar
COPY shell/package.json /src/shell/package.json
COPY sidecar/package.json sidecar/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY sidecar/ ./
RUN pnpm upstream && pnpm build

# The Go wrapper.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /out/open-slide-mcp ./cmd/open-slide-mcp

# Runtime: the wrapper, the static shell, the bundled modules, and Node for
# the sidecar. No dev server, no package manager.
FROM node:22-bookworm-slim
WORKDIR /app
COPY --from=shell /src/shell/dist ./shell/dist
COPY --from=shell /out/modules ./modules
COPY --from=sidecar /src/sidecar/dist ./sidecar/dist
COPY --from=build /out/open-slide-mcp /usr/local/bin/open-slide-mcp
RUN mkdir -p workspace/slides workspace/.open-slide-mcp && chown -R node:node /app
USER node
ENV WORKSPACE_DIR=/app/workspace \
    SHELL_DIR=/app/shell/dist \
    MODULES_DIR=/app/modules \
    SIDECAR_DIR=/app/sidecar/dist \
    LISTEN_ADDR=:8090
EXPOSE 8090
VOLUME ["/app/workspace/slides", "/app/workspace/.open-slide-mcp"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s CMD node -e "fetch('http://127.0.0.1:8090/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"
ENTRYPOINT ["open-slide-mcp"]
