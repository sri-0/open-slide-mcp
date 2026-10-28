# shell

A one-off static build of open-slide's editor app that loads separately compiled decks at
runtime. The Go wrapper serves `dist/` as the app and compiles each deck on its own with
esbuild; nothing here runs a dev server.

## Build

```sh
pnpm install
pnpm build          # node gen-vendor.mjs && vite build  -> dist/
pnpm exec tsc --noEmit
```

`dist/index.html` carries an import map and the hot shim ahead of the app's module script;
`dist/.vite/manifest.json` maps entries to hashed files. `pnpm sync:skills` refreshes the
skills copy from `@open-slide/core`.

## Contract with open-slide (pinned at `@open-slide/core` 2.2.0)

Virtual modules the pre plugin in `plugin.ts` serves instead of open-slide's:

| id | what the shell exports |
| --- | --- |
| `virtual:open-slide/config` | `{ base: '/', build: { showSlideBrowser: false, showSlideUi: true, allowHtmlDownload: true }, version }` (no `locale`) |
| `virtual:open-slide/slides` | reads `/manifest.json` at load: `slideIds`, `slideThemes`, `slideCreatedAt`, `loadSlide(id)` importing `url + '?t=' + token`; unknown id rejects with `Slide not found: <id>` |
| `virtual:open-slide/folders` | `{ folders: [], assignments: {} }` |
| `virtual:open-slide/themes` | `themes = []`, `loadThemeDemo` rejects |

Manifest shape: `{"slides":[{"id","title","theme","createdAt":"<RFC3339>","url":"/d/<hash>/index.js"}]}`.

DEV gates: the build defines `import.meta.env.DEV` true and `PROD` false so the editor stays
in, and `import.meta.hot` as `globalThis.__osHot` so its 23 HMR sites attach to the shim in
`src/hot.ts`. The shim opens `EventSource('/__events')`; each SSE `event:` name is emitted to
listeners with its JSON `data:`. Names the editor uses: `open-slide:slide-changed`
(`{slideIds}`; bumps the import token so `loadSlide` re-imports), `open-slide:files-changed`,
`open-slide:assets-changed`, `vite:afterUpdate`; `vite:ws:connect` and `vite:ws:disconnect`
follow the stream's open and error. The only `send` with a receiver is `open-slide:current`,
posted as JSON to `/__current`.

Import map, from the vendor entries `gen-vendor.mjs` writes into `vendor/` (committed:
`react-jsx-dev-runtime.js` only): `react`, `react/jsx-runtime`, `react/jsx-dev-runtime`,
`react-dom`, `react-dom/client` export every name of the installed package (React's CJS
`module.exports` is also the default export); `@open-slide/core` exports the names on its
entry's export list: `CANVAS_HEIGHT`, `CANVAS_WIDTH`, `ImagePlaceholder`, `MorphElement`,
`Step`, `Steps`, `cssVarsToString`, `defaultDesign`, `designToCssVars`, `useIsActivePage`,
`useSlidePageNumber`. `react/jsx-dev-runtime` is hand-written: its `jsxDEV` wraps `jsx`/`jsxs`
and tags host elements and `ImagePlaceholder` with `data-slide-loc="line:column"` (esbuild's
1-based column minus one, matching Babel), which is how the inspector selects elements.

`lucide-react` is a dependency only so the Go compiler can bundle it into decks from
`node_modules`; the shell does not import it.
