# open-slide-mcp

A Go wrapper that turns an [open-slide](https://open-slide.dev) workspace into a
service: it supervises the open-slide dev server, proxies it behind signed
preview links, and exposes deck authoring as MCP tools. Agents write decks
through the tools; users see and edit them in the proxied open-slide UI, which
can be iframed into another app.

```
agent (opencode / agentic) --MCP streamable HTTP--> :8090/mcp
browser / iframe            --preview link-------> :8090/preview/<deck>  --> vite :5173
```

## Tools

| Tool | Purpose |
|---|---|
| `deck_create` | Create a deck. `thread_id` is required and owns the deck. Returns `deck_id`, `hash`, `created_at`, `preview_url`. |
| `deck_write` | Replace the deck's `index.tsx`. Pass `expected_hash` so browser edits are never clobbered. The module is compiled by Vite and errors come back with line and column. |
| `deck_read` | Source, hash, metadata and any `@slide-comment` markers left in the inspector. |
| `deck_list` | Decks owned by a `thread_id`. |
| `deck_validate` | Compile without writing. |
| `deck_preview_url` | Fresh preview link for embedding. |
| `deck_current` | The live cursor: which deck, page and element the user is looking at. |
| `deck_delete`, `thread_cleanup` | Remove one deck, or every deck a thread owns. |
| `skill_read` | The open-slide authoring skills, adapted for MCP use. Read `slide-authoring` first. |

Skills are also published as resources (`openslide://skills/<name>`) and there
is a `create-slide` prompt that bundles the workflow and the reference.

Idle decks are swept after `DECK_TTL` (default 24h).

## Run locally

```
make setup     # pnpm install inside ./workspace (needs Node 20.19+ and pnpm)
make run       # builds and starts on :8090, spawns the dev server on :5173
make smoke     # drives every tool through the MCP endpoint
```

## Run in Docker

```
make docker-build
make docker-run   # or: docker compose up
make smoke
```

Slides and metadata live in named volumes so they survive restarts.

## Connect opencode

Add to `~/.config/opencode/opencode.json` (`make opencode-config` prints it):

```json
"mcp": {
  "open-slide": { "type": "remote", "url": "http://localhost:8090/mcp", "enabled": true }
}
```

Then `opencode mcp list` should show `open-slide connected`. Ask the agent to
call `skill_read` for `slide-authoring`, then `deck_create` with a thread id.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `:8090` | wrapper listen address |
| `MCP_PATH` | `/mcp` | MCP endpoint path |
| `MCP_TRANSPORT` | `http` | `http` or `stdio` |
| `WORKSPACE_DIR` | `./workspace` | open-slide workspace |
| `PUBLIC_URL` | `http://localhost:8090` | base of preview links |
| `VITE_HOST` / `VITE_PORT` | `127.0.0.1` / `5173` | dev server bind |
| `MANAGE_DEVSERVER` | `true` | spawn the dev server; `false` attaches to one you run |
| `PREVIEW_AUTH` | `false` | require signed tokens for the preview UI |
| `PREVIEW_SECRET` | random | HMAC key for tokens; set it for stable links across restarts |
| `PREVIEW_TTL` | `12h` | token lifetime |
| `DECK_TTL` | `24h` | idle deck lifetime; `0` disables sweeping |
| `SWEEP_INTERVAL` | `10m` | sweeper cadence |

With `PREVIEW_AUTH=true` the preview link carries a token, the proxy exchanges
it for a cookie scoped to that deck, and `/s/<other-deck>` is refused. The
dev-server endpoints that restart the process or create and delete slides from
the browser are always blocked.

## Limits

- Validation catches syntax and import errors, not type errors.
- Assets cannot be uploaded through the MCP yet.
- One dev server serves every deck. For hard tenant isolation run one
  container per tenant.
- PPTX and PDF export are buttons in the preview UI, not tools.
