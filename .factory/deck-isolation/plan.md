# Deck isolation plan

Full plan, with the research, posture, options and design, is the annotatable page:
https://claude.ai/artifact/82JQvfyTjgmB7CrzqMxt4Z (28 October 2026).

## Verdict

The proposal is feasible. Nothing the LLM writes runs in Node today; the problems are one
shared origin, one shared Vite dev server that broadcasts every error and reload to everyone,
and an MCP endpoint with no identity. Build the open-slide app once, compile each deck with
esbuild in a child process of the Go binary (parse and transform only, deadline and memory
limit), serve each deck on its own origin in a sandboxed iframe, and have the wrapper own
every endpoint the editor calls.

## Findings that shape the design

- A deck is an ESM module (`default: Page[]` plus `meta`, `design`, `notes`, `transition`)
  importing only `react` and `@open-slide/core`. Tailwind never scans `slides/`; decks are
  inline-styled. No per-deck CSS is needed.
- Deck and shell must share one React and one runtime instance (contexts on `globalThis`), so
  deck bundles externalise `react`, `react/jsx-runtime`, `@open-slide/core` via an import map.
- The editor must share the deck's document (fibers, `contenteditable`, MutationObserver), so
  the whole app runs inside the per-deck frame. Editing is gated on `import.meta.env.DEV`
  (36 sites); the shell build flips them with Vite `define` (proven in a headless Chrome run). Loc tags are a dev-only Babel transform that the package does not export
  (pure function). Edit routes are Babel AST splices in `src/editing/*` (about 90 KB, MIT).
- The dev server never executes slide code; it executes only `open-slide.config.ts`. It does
  serve any workspace file via `/@fs`, broadcast one deck's errors to all clients, and expose
  spawn, restart and egress routes behind an Origin check. 2.3.0 adds agent-running routes.
- esbuild never evaluates input but is not a sandbox: a 4 MB nested input overflowed the Go
  stack and killed the process. Hence the child process.

## Posture today (ranked)

1. Deck JS can read, rewrite and delete other decks through `/__edit`, `/__slides/:id`, `/@fs`.
2. Proxy deny list is exact-match; `DELETE /__slides/<id>` and friends are forwarded.
3. MCP has no identity; `thread_id` is advisory; `deck_list` with an empty thread lists all.
4. `PREVIEW_AUTH` off by default; when on, scoped to `/s/<id>` HTML only.
5. One deck's error or lifecycle event interrupts every user; a hung Vite is invisible.
6. `deck_current` is one global, forgeable cursor.

## Options

A. Harden the shared dev server (allow list, per-request deck check): small, keeps one origin.
B. One dev server per tenant in a container: isolation by weight; ruled out (no dev servers).
C. Prebuilt shell, per-deck compile, per-deck origin, Go-owned routes: recommended.
D. Compile in the browser with esbuild-wasm: no gain over C, 14 MB per frame.

Recommendation: C, built viewer-first, with A's allow list as phase 1.

## Phases

1. Allow-list the proxy by prefix, check `slideId` on every `/__*` request, refuse `/@fs`
   outside the session's deck, auth on by default.
2. esbuild compile in a child (`open-slide-mcp compile`), bundle store, shell built once by our
   Vite config, per-deck origins, viewer and presenter working, dev server removed.
3. Editor routes in Go plus a parse-only Node sidecar for loc tags and source patches;
   per-deck SSE stream.
4. Authenticated `/mcp`, owner on every store call, UUIDv7 ids, per-session cursor.

## Decisions pending

Phase order (viewer-first or editor-first); vendor `src/editing/*` or wait for an upstream
export; wildcard origins versus opaque sandboxed frames; what token `/mcp` callers carry; drop
or proxy Google Fonts and svgl; accept a Node sidecar or require a Go port first.
