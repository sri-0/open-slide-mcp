# The editor shell: open-slide's app built once into static files, plus the
# pinned copy of lucide-react the compiler bundles into decks.
FROM node:22-bookworm-slim AS shell
RUN corepack enable && corepack prepare pnpm@9.7.1 --activate
WORKDIR /src/shell
COPY shell/package.json shell/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY shell/ ./
RUN pnpm build && mkdir -p /out/modules && cp -rL node_modules/lucide-react /out/modules/lucide-react

# The editing functions, fetched from the open-slide repository tag matching
# the shell's pinned version and bundled twice: for the Go binary to embed,
# and as the optional Node sidecar.
FROM node:22-bookworm-slim AS edits
RUN corepack enable && corepack prepare pnpm@9.7.1 --activate
WORKDIR /src/edits
COPY shell/package.json /src/shell/package.json
COPY edits/package.json edits/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY edits/ ./
RUN pnpm upstream && pnpm build

# The Go wrapper, with the embedded editing bundle in place before it builds.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=edits /src/pkg/edits/apply.js pkg/edits/apply.js
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /out/open-slide-mcp ./cmd/open-slide-mcp

# Runtime: the wrapper, the static shell and the bundled modules. Node is
# present only for the optional sidecar engine (EDIT_ENGINE=sidecar).
FROM node:22-bookworm-slim
WORKDIR /app
COPY --from=shell /src/shell/dist ./shell/dist
COPY --from=shell /out/modules ./modules
COPY --from=edits /src/edits/dist ./edits/dist
COPY --from=build /out/open-slide-mcp /usr/local/bin/open-slide-mcp
RUN mkdir -p workspace/slides workspace/.open-slide-mcp && chown -R node:node /app
USER node
ENV WORKSPACE_DIR=/app/workspace \
    SHELL_DIR=/app/shell/dist \
    MODULES_DIR=/app/modules \
    SIDECAR_DIR=/app/edits/dist \
    LISTEN_ADDR=:8090
EXPOSE 8090
VOLUME ["/app/workspace/slides", "/app/workspace/.open-slide-mcp"]
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s CMD node -e "fetch('http://127.0.0.1:8090/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"
ENTRYPOINT ["open-slide-mcp"]
