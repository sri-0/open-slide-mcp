# Build record: deck isolation

Branch `feat/deck-isolation` (off `feat/keep-decks`), 29 October 2026. Nothing committed.

## What was built

| Area | Change |
| --- | --- |
| `internal/compile` | esbuild (Go library) in a child process of this binary (`open-slide-mcp compile`): deadline, RLIMIT_AS/CPU, stack cap, input byte cap, import allow list (external names through the shell's import map, `lucide-react` bundled from `shell/node_modules`), named imports from externals checked against `shell/dist/exports.json`, metafile second gate |
| `shell/` | open-slide 2.2.0's editor built once with our Vite config: pre-plugin replaces the four virtual modules (runtime manifest, gallery off), `define` keeps the editor in and routes `import.meta.hot` to an SSE shim, vendor entries with explicit names feed the import map, a `react/jsx-dev-runtime` shim adds `data-slide-loc` from esbuild's JSX positions |
| `sidecar/` | parse-only Node service running open-slide's vendored Babel edit functions (tag 2.2.0) behind one `/apply` endpoint, under `node --permission --allow-fs-read=<dist>` with a minimal environment |
| `internal/shell` | each deck on `DECK_ORIGIN` (`http://<uuid>.decks.localhost:8090` locally): link token exchanged for a `__Host-` session cookie (HttpOnly, Secure, SameSite=None, Partitioned), every route checked against the host's deck, manifest, stable `/d/index.js` with ETag, CSP with per-request nonce, Permissions-Policy |
| `internal/auth` | HS256 deck tokens with a `use` claim (link vs session); OIDC bearer verification (go-oidc) and `X-Root-Thread-ID` requirement on `tools/call` when `MCP_AUTH=oidc` |
| `internal/editor` | the editor's `/__*` save routes: Go reads the source, the sidecar patches it, Go writes, recompiles and publishes; `/__slides/<other>` refused |
| `internal/events` | per-deck SSE broker replacing the Vite HMR socket |
| `internal/mcpserver` | UUIDv7 deck ids, ownership from the root-thread header, `deck_current` per deck or thread, `deck_preview_url` returns `deck_id` and `title` |
| removed | `internal/devserver`, `internal/proxy`, `internal/validate`, the workspace's `package.json`, `open-slide.config.ts`, `.agents/`, all unit tests except none (owner's instruction) |
| docs | README rewritten; `shell/README.md`, `sidecar/README.md` |

## Proof

- `make smoke`: MCP lifecycle, preview link exchange, module served, session refused on another origin. `SMOKE OK`.
- `make isolation` (`scripts/isolation.sh`): 37 curl checks, all passing: tokens, cookie attributes, cross-origin and cross-deck refusals, CSP and import map, import allow list, unknown named export refused, size cap, server health, browser edit through the sidecar with recompile, cross-deck page route refused, per-deck cursor, cleanup.
- Headless Chrome against the real wrapper: link exchange renders the editor with an HttpOnly cookie, another deck's origin shows "unknown deck", reload without the token works, an MCP write re-imports the module live (`/d/index.js?t=…` 200) with no page reload, clicking a heading selects it and posts the cursor, a style edit saved through `/__edit` recolours the heading.
- agentlite + agentui, `MCP_AUTH=oidc` against the local Keycloak: the assistant created a deck through the slides connector, hit the import allow list on a hallucinated package, read the message and rewrote; agentui's side panel iframed `http://<uuid>.decks.localhost:8090/?t=…&theme=dark`; the deck is listed only for agentlite's root thread; tool calls without a token, with a forged token, without the thread header, or naming another thread are refused.
- Compile package proof program (scratchpad): a 2 MiB nested input kills the child, not the server.

## Not done / follow-ups

- agentlite stores `preview_url` once; `deck_preview_url` now returns `deck_id` and `title` so agentlite's `artifacts:` config can record refreshed links (a one-line change there).
- agentlite's library copy of the skills still teaches `./assets` imports and Google Fonts; re-upload from `internal/skills/data` after reviewing the preface.
- `PREVIEW_SECRET` must be set in production or stored links die on restart.
- Trust in `X-Root-Thread-ID` rests on the caller holding a valid user token; binding it cryptographically needs agentlite's token-exchange plan.

## Review (code-reviewer, 29 October 2026) and fixes

| Finding | Severity | Fix |
| --- | --- | --- |
| A JSON-RPC batch body (`[...]`) slipped past the bearer gate because the single-object probe failed silently | critical | the gate refuses arrays and non-JSON bodies with 400 before the method check; proven with curl in oidc mode |
| Mutating deck routes had no Origin or content-type check on a SameSite=None cookie | major | writes on a deck origin must carry the deck's own `Origin` or `Sec-Fetch-Site: same-origin`, and `application/json`; proven with curl and the browser's own posts |
| A malformed edit body returned 502 with a Go error | minor | `json.Valid` check, 400 |
| Preview-link token appeared in debug request logs | minor | the logger records the path only |
| "exchanged once" overstated a reusable capability link | minor | wording fixed in code, tool description and README |
| Ownership rests on `X-Root-Thread-ID`, which any valid-token holder reaching `/mcp` could set | major, by design | documented in the README: keep `/mcp` reachable only from agentlite; binding the thread into the token needs agentlite's token-exchange plan |
| `a.txt` at the repo root is the owner's pasted request | nit | left for the owner; not for the commit |

Static checks after the fixes: gofmt, go vet, staticcheck clean; `pnpm exec tsc --noEmit` clean in `shell/`; `make smoke` OK; `make isolation` 41 of 41.

## Follow-up (29 October 2026, later)

- **Create/delete loop in agentui.** The model passed the id of a markdown artifact it had just emitted as `thread_id`, because the description said "thread_id (or an artifact id)". agentlite overwrote it with the root thread id, the result echoed a different `thread_id`, and the model deleted and recreated the deck seven times in silence. Fixes: the description now says the thread id is the conversation's, never an artifact id; the result no longer echoes `thread_id`; `deck_delete` stays in the code but is disabled by default through `MCP_TOOLS_DISABLED` (a list), and the server instructions say decks are changed with `deck_write`, never recreated.
- **Deck URL layout.** `DECK_ORIGIN` now accepts `{deck}` as the first host label (one origin per deck) or as the path (`http://localhost:8090/{deck}`, one shared origin). Path mode needed: a path-aware origin model, a `<meta name="osmcp-base">` the page injects, a shell transform that rewrites open-slide's 23 absolute `/__*` fetches onto that base, `import.meta.env.BASE_URL` defined to the runtime base, a `__Secure-` cookie scoped to the deck's path, and public shell assets at the shared root. The README states what path mode gives up: same-origin decks can reach each other's routes with the user's cookies and each other's frames.
- Proof: `make smoke` and `make isolation` (41 checks) pass in both modes; headless Chrome in path mode rendered the editor under `/<uuid>/s/<uuid>`, selected a heading, posted the cursor and saved a style edit.
- Nothing is committed or pushed. The only upstream code copied into the repo is `sidecar/vendor/open-slide` (MIT, tag 2.2.0); the shell builds from `node_modules`.

## Layout (29 October 2026, before the pull request)

- Shareable code moved under `pkg/`: `pkg/compile` (the esbuild child and allow list, which imports nothing from `internal/`), `pkg/events` (the SSE broker) and `pkg/auth` (deck tokens, the OIDC bearer gate). `internal/` keeps what is specific to this service: `build`, `config`, `editor`, `mcpserver`, `shell`, `skills`, `workspace`. The compiler is reached from the MCP tools only through `internal/build`.
- The sidecar no longer vendors open-slide's editing sources. `pnpm upstream` (`sidecar/fetch-upstream.mjs`) downloads the repository tag matching the version `shell/package.json` pins into the gitignored `sidecar/upstream/`, naming each of the 15 files it takes, and fails if one has moved. One version bump upgrades the shell and the sidecar together.
