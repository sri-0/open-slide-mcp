# open-slide-mcp

A Go service that lets agents write [open-slide](https://open-slide.dev) decks
through MCP tools and lets people view and edit them in the open-slide editor,
with every deck compiled on its own and served on its own origin. Nothing an
agent writes is ever executed on the server.

```
agent (agentlite / opencode) --MCP streamable HTTP--> :8090/mcp
browser / iframe ------------- preview link ---------> http://<deck-id>.decks.localhost:8090
```

## How it works

- **Compile, never run.** `deck_write` hands the deck's `index.tsx` to esbuild's
  Go library in a child process of this binary (`open-slide-mcp compile`) with
  a deadline, a memory limit and an input size cap. esbuild parses and
  transforms; it has no JavaScript engine. A deck may import only the names in
  `DECK_IMPORTS_EXTERNAL` (resolved in the browser through an import map) and
  `DECK_IMPORTS_BUNDLED` (bundled from a pinned copy under `MODULES_DIR`);
  every other import, relative or bare, is a compile error. A deck that fails
  to compile is kept so the agent can fix it, and the preview keeps serving the
  last good build.
- **One shell, built once.** `shell/` builds the open-slide editor from the
  source the package ships, with a small Vite plugin that swaps open-slide's
  build-time virtual modules for a runtime manifest and a server-sent event
  stream, keeps the editor in, and adds a `react/jsx-dev-runtime` shim that
  tags elements with the source positions the inspector needs. The result is
  static files, the same for every deck and every user.
- **One origin per deck, or one shared origin.** `DECK_ORIGIN` decides. With
  `{deck}` as the first host label (`https://{deck}.decks.example.com`) each
  deck is reachable only on its own host, only with a session cookie minted
  for that deck, and the browser's same-origin policy keeps decks apart from
  each other and from the embedding app. With `{deck}` as the path
  (`https://decks.example.com/{deck}`) decks share one origin and simpler
  infrastructure, and are kept apart by their path-scoped sessions only: a
  deck's JavaScript can then reach another deck's routes with that deck's
  cookie if the user has it, and two decks open in one page can read each
  other's frames. Choose the host form wherever decks come from people you do
  not trust with each other's decks.
- **Signed access.** `preview_url` carries a short-lived signed link token.
  Opening it sets a host-only `__Host-` session cookie (HttpOnly, Secure,
  SameSite=None, Partitioned) that lives for `SESSION_TTL`, so a link the
  embedding app stored keeps working after the link itself has expired. A link
  token is never accepted as a cookie, nor a cookie as a link, and writes on a
  deck origin are accepted only from the deck's own page.
- **Edits without a dev server.** The editor's save routes are served by Go.
  Source patches (visual edits, comments, notes, design, page order) go to a
  parse-only Node sidecar that runs open-slide's own Babel edit functions on
  the text under Node's permission model, with no filesystem or network access
  beyond its bundle. Go writes the result, recompiles and notifies open editors.

## Tools

| Tool | Purpose |
|---|---|
| `deck_create` | Create a deck. `thread_id` is required and owns the deck. Returns `deck_id` (a UUIDv7), `hash`, `created_at`, `preview_url`. |
| `deck_write` | Replace the deck's `index.tsx`. Pass `expected_hash` so browser edits are never clobbered. Compile errors come back with line and column. |
| `deck_read` | Source, hash, metadata and any `@slide-comment` markers left in the inspector. |
| `deck_list` | Decks owned by a `thread_id`. |
| `deck_validate` | Compile the current source without changing anything. |
| `deck_preview_url` | Fresh preview link for embedding, with `deck_id` and `title`. |
| `deck_current` | The live cursor for a deck, or the newest across a thread's decks. |
| `thread_cleanup` | Remove every deck a thread owns, only when the user asks to discard them. |
| `deck_delete` | Remove one deck. Disabled by default through `MCP_TOOLS_DISABLED`, because a model that can delete will delete to retry. |
| `skill_read` | The open-slide authoring skills, adapted for MCP use. Read `slide-authoring` first. |

Skills are also published as resources (`openslide://skills/<name>`) and there
is a `create-slide` prompt that bundles the workflow and the reference.

### Ownership

With `MCP_AUTH=oidc`, every `tools/call` must carry a bearer token issued by
`OIDC_ISSUER_URL` for `OIDC_AUDIENCE`, and an `X-Root-Thread-ID` header naming
the caller's thread; agentlite sends both. That thread owns every deck it
creates, and a deck of another thread is reported as not found. `initialize`
and `tools/list` need no token so a client can discover the server.
With `MCP_AUTH=none` the endpoint is open and ownership is the `thread_id`
argument the caller supplies; use it only for local work.

The token proves a user of the realm, not the thread: any holder of a valid
token who can reach `/mcp` directly may name any thread in the header. Keep
`/mcp` reachable only from agentlite, which sets the header from the run it
is executing. Binding the thread into the token is agentlite's token-exchange
plan, not something this server can check.

## Run locally

Needs Go 1.25, Node 22+ and pnpm.

```
make setup     # pnpm install in shell/ and sidecar/
make shell     # build the editor once into shell/dist
make sidecar   # bundle the sidecar into sidecar/dist/server.js
make run       # :8090, decks on http://<deck>.decks.localhost:8090 (DECK_ORIGIN overrides)
make smoke     # drives every tool and opens a preview
make isolation # curl proof: tokens, cookies, cross-deck refusals, compiler limits, editor round trip
```

Browsers resolve `*.localhost` to loopback and treat it as a secure context,
which is why the Secure cookie works over http there. `make run` uses
`MCP_AUTH=none`; set `MCP_AUTH=oidc` with the `OIDC_*` variables to run against
the agentlite stack.

## Run in Docker

```
make docker-build
make docker-run   # or: docker compose up
```

Slides, builds and metadata live in named volumes. Set `PREVIEW_SECRET` so
preview links stored by the embedding app survive restarts. The compose file
joins the agentlite stack's network so it can verify tokens against Keycloak.

## Embed the preview

`preview_url` is meant to be iframed from one of `EMBED_ORIGINS`, which also
becomes the page's `frame-ancestors`. Give the frame
`sandbox="allow-scripts allow-same-origin allow-popups allow-downloads"` and
`allow="fullscreen"`; `allow-same-origin` is safe because the frame is
cross-origin to the embedder and is needed for the editor's own requests to
carry the cookie.

The embedding page can set the editor's light/dark mode in two ways:

- **On load:** add `theme=light|dark|system` to the preview link, e.g.
  `…/?t=<token>&theme=dark`.
- **Live:** post a message to the iframe whenever the embedder's theme changes.

  ```js
  iframe.contentWindow.postMessage(
    { type: "open-slide:theme", theme: "dark" }, // "light" | "dark" | "system"
    new URL(previewUrl).origin,
  );
  ```

open-slide only reads the theme from `localStorage["theme"]`, so the page
carries a small script that stores the parameter or message value there and
fires the `storage` event the UI listens for. Messages are accepted only from
`EMBED_ORIGINS`; empty accepts any origin.

## Connect opencode

Add to `~/.config/opencode/opencode.json` (`make opencode-config` prints it):

```json
"mcp": {
  "open-slide": { "type": "remote", "url": "http://localhost:8090/mcp", "enabled": true }
}
```

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `:8090` | listen address for the MCP endpoint and every deck origin |
| `MCP_PATH` | `/mcp` | MCP endpoint path |
| `MCP_TRANSPORT` | `http` | `http` or `stdio` |
| `MCP_AUTH` | `none` | `oidc` requires a verified bearer token and `X-Root-Thread-ID` on tool calls |
| `OIDC_ISSUER_URL`, `OIDC_AUDIENCE` | | the `iss` tokens must carry and the `aud` they must contain (`oidc` only) |
| `OIDC_DISCOVERY_URL` | issuer | where provider metadata is fetched when the issuer is not reachable by that name |
| `WORKSPACE_DIR` | `./workspace` | holds `slides/`, compiled builds and the metadata index |
| `DECK_ORIGIN` | `http://{deck}.decks.localhost:8090` | `{deck}` as the first host label (one origin per deck) or as the path, `http://localhost:8090/{deck}` (one shared origin); https unless the host is localhost |
| `MCP_TOOLS_DISABLED` | `deck_delete` | tools that exist but are not offered to agents |
| `COOKIE_NAME` | `osmcp` | name of the deck session cookie, served as `__Secure-<name>` |
| `SHELL_DIR` | `./shell/dist` | the built editor |
| `MODULES_DIR` | `./shell/node_modules` | where bundled deck imports are read from |
| `DECK_IMPORTS_EXTERNAL` | `react,react/jsx-runtime,react/jsx-dev-runtime,@open-slide/core` | imports the shell provides |
| `DECK_IMPORTS_BUNDLED` | `lucide-react` | imports bundled into each deck |
| `COMPILE_TIMEOUT` | `10s` | deadline for one compile |
| `COMPILE_MAX_BYTES` | `262144` | largest deck source accepted |
| `COMPILE_MAX_MEMORY` | `1073741824` | address-space limit of the compile child (Linux) |
| `SIDECAR_DIR` | empty | the built sidecar; empty disables browser edits |
| `SIDECAR_ADDR` | `127.0.0.1:8091` | loopback address the sidecar listens on |
| `PREVIEW_SECRET` | random | HMAC key for deck tokens; set it for stable links across restarts |
| `PREVIEW_TTL` | `12h` | lifetime of a preview link |
| `SESSION_TTL` | `168h` | lifetime of the session cookie a link is exchanged for |
| `DECK_TTL` | `0` | idle deck lifetime; `0` keeps decks until deleted |
| `SWEEP_INTERVAL` | `10m` | sweeper cadence |
| `EMBED_ORIGINS` | empty | comma-separated origins allowed to embed decks and set the theme; empty allows any, with a startup warning |

## Upgrade open-slide

`shell/package.json` pins `@open-slide/core` (currently 2.2.0), and that one
version drives everything: the shell is built from the installed package, and
`make sidecar` fetches the matching repository tag for the editing functions
the package does not export (the list is in `sidecar/fetch-upstream.mjs`; a
file that moves upstream fails the fetch). To move to a newer release:

```
cd shell && pnpm up "@open-slide/core@<version>" && cd ..
make shell sidecar
cd shell && pnpm sync:skills && rsync -a --delete .agents/skills/ ../internal/skills/data/
```

Then re-check the pinned contract listed in `shell/README.md`: the virtual
module ids, the `DEV` gates, the `/__*` routes and the hot event names. Run
`make smoke` and `make isolation`.

## Limits

- Validation catches syntax errors and disallowed imports, not type errors.
- Decks cannot import assets; use inline SVG, CSS or `ImagePlaceholder`.
- External web fonts are blocked by the deck page's content security policy.
- Google Fonts and svgl lookups, package updates and server restart do not
  exist; the editor's buttons for them fail quietly.
- The slide gallery is off: a deck's origin knows only that deck.
- PPTX and PDF export are buttons in the preview UI, not tools.
