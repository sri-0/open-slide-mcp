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

Decks are kept until deleted (`deck_delete`, `thread_cleanup`); set `DECK_TTL` to
sweep decks idle for longer than it.

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

## Embed the preview

`preview_url` is meant to be iframed. The embedding page can set the editor's
light/dark mode in two ways:

- **On load:** add `theme=light|dark|system` to the preview link, e.g.
  `http://localhost:8090/preview/<deck>?theme=dark` (with `PREVIEW_AUTH` the
  link already has `?t=…`, so append `&theme=dark`). The redirect to
  `/s/<deck>` keeps the parameter and the page paints in that theme.
- **Live:** post a message to the iframe whenever the embedder's theme changes.
  The editor switches without reloading.

  ```js
  iframe.contentWindow.postMessage(
    { type: "open-slide:theme", theme: "dark" }, // "light" | "dark" | "system"
    new URL(previewUrl).origin,
  );
  ```

A message only reaches a loaded page, so rely on `?theme=` for the first paint
(or post again from the iframe's `load` handler). Any other `theme` value, and
any other message, is ignored. Set
`EMBED_ORIGINS` to accept theme messages only from the listed embedder origins.

open-slide itself only reads the theme from `localStorage["theme"]`, so the
wrapper injects a small script at the top of every HTML page it proxies: it
stores the parameter or message value there and fires the `storage` event the
UI listens for. The value persists for the wrapper's origin, so opening the
editor directly afterwards uses the last theme set. Browsers that partition
storage in cross-site iframes keep a separate value per embedding site.

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
| `DECK_TTL` | `0` | idle deck lifetime; `0` keeps decks until deleted |
| `SWEEP_INTERVAL` | `10m` | sweeper cadence |
| `EMBED_ORIGINS` | empty | comma-separated origins (e.g. `http://localhost:3000`) allowed to set the theme by `postMessage`; empty accepts any origin |

With `PREVIEW_AUTH=true` the preview link carries a token, the proxy exchanges
it for a cookie scoped to that deck, and `/s/<other-deck>` is refused. The
dev-server endpoints that restart the process, update packages, or create and
delete slides from the browser are always blocked.

## Upgrade open-slide

The workspace pins `@open-slide/core` in `workspace/package.json` and
`workspace/pnpm-lock.yaml` (currently 2.2.0). To move to a newer release:

```
cd workspace
pnpm up "@open-slide/core@^<version>"
pnpm sync:skills                  # refreshes .agents/skills; delete the .claude/ symlinks it creates
cd .. && rsync -a --delete workspace/.agents/skills/ internal/skills/data/
make test && make run && make smoke
```

`internal/skills/data` is the copy served by `skill_read`; the MCP preface in
`internal/skills/skills.go` maps the skills' filesystem steps onto the tools.

## Limits

- Validation catches syntax and import errors, not type errors.
- Assets cannot be uploaded through the MCP yet.
- One dev server serves every deck. For hard tenant isolation run one
  container per tenant.
- PPTX and PDF export are buttons in the preview UI, not tools.
