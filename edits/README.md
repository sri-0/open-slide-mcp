# edits

open-slide's editor save routes as one pure function, `apply(request) ->
response`, built two ways from the same sources: `pnpm build:embedded` writes
`../pkg/edits/apply.js`, which the Go binary embeds and runs with goja (the
default `EDIT_ENGINE=embedded`); `pnpm build:sidecar` writes `dist/server.js`,
a loopback Node server for `EDIT_ENGINE=sidecar`. The embedded bundle adds a
small `Buffer` polyfill and a `crypto.randomUUID` shim (`src/embedded/`), and
was proven byte-identical to Node over 1,124 request pairs.


A parse-only Node process that applies the open-slide editor's source patches. The browser
editor saves visual edits as requests such as `POST /__edit {slideId, line, column, ops}`,
which open-slide's dev server applied to `slides/<id>/index.tsx` with Babel AST splices. The
Go server now owns those routes, authentication and file I/O; it forwards the browser's
request and the deck's current source here, and this process returns the browser's reply and
the new source. It never reads or writes files, spawns processes or makes network calls: it
is a pure text transformer behind a loopback HTTP server, run under Node's permission model.

## Contract

`POST /apply` with JSON:

```json
{
  "method": "POST",
  "path": "/__edit",
  "query": { "slideId": "..." },
  "body": { "slideId": "...", "line": 117, "column": 4, "ops": [{ "kind": "set-style", "key": "color", "value": "#ff0000" }] },
  "source": "<current index.tsx>"
}
```

replies `200` with JSON `{ "status": 200, "body": { "ok": true, "changed": true }, "source": "<new index.tsx>" }`.
`status` and `body` are exactly what the dev server sent the browser (400 `{error}` on bad
input, 404 for an unknown marker, 422 when the rewrite is unsafe, 500 on a thrown error).
`source` is present only when the route changed the source, so Go writes the file only then.
`body` is the browser's JSON body, or `null`; `query` holds the URL's query parameters.

| Route | Body | Reply body |
| --- | --- | --- |
| `POST /__edit` | `{slideId, line, column?, ops}` | `{ok, changed}` |
| `POST /__edit/batch` | `{slideId, edits: [{line, column?, ops, dependsOn?}]}` | `{ok, changed, results: [{ok, error?}]}` |
| `POST /__edit/revert-asset` | `{slideId, assetPath}` | `{ok, changed}` |
| `GET /__comments?slideId=` | | `{comments: [{id, line, ts, note, hint?, rect?, page?, target}]}` |
| `POST /__comments/add` | `{slideId, line, column?, text, hint?, rect?, page?}` | `{id, line}` |
| `PUT /__comments/:id` | `{slideId, text}` | `{ok}` |
| `DELETE /__comments/:id?slideId=` | | `{ok}` |
| `GET /__design?slideId=` | | `{design, exists, warning}` |
| `PUT /__design?slideId=` | `{patch}` | `{ok, design, created}` |
| `POST /__design/reset?slideId=` | | `{ok, design, created}` |
| `PUT /__notes` | `{slideId, index, text}` | `{ok, changed}` |
| `PUT /__slides/:id/reorder` | `{order: number[]}` | `{ok, slideId, order}` |
| `POST /__slides/:id/pages/:i/duplicate` | | `{ok, slideId, index}` |
| `DELETE /__slides/:id/pages/:i` | | `{ok, slideId, index}` |
| `PATCH /__slides/:id` | `{name}` | `{ok, slideId, name}` |

Anything else is `404 {error: "not found"}`. `GET /healthz` replies `{ok: true}`. The request
body is capped at 4 MiB (`413`). One line per request goes to stderr: method, path, status
and milliseconds, never the source.

Not here, because they need the disk or the HTTP headers: `slide not found` (Go reads the
file), the origin and content-type guard (`403`/`415`), and the directory routes
`POST /__slides/:id/duplicate` and `DELETE /__slides/:id`.

## Build and run

```sh
pnpm install
pnpm upstream  # downloads the open-slide sources at the version shell/package.json pins into upstream/ (not committed)
pnpm build     # both bundles: ../pkg/edits/apply.js for the Go binary and dist/server.js for the sidecar
pnpm start     # node --permission --allow-fs-read=./dist dist/server.js
```

The runtime needs only Node and `dist/server.js`. `SIDECAR_ADDR` sets the listen address
(default `127.0.0.1:8091`); the process refuses to start on anything but a loopback address.
`--permission` is the stable flag on current Node (checked on 23.9); older releases spell it
`--experimental-permission`.

To exercise it by hand:

```sh
jq -n --rawfile s slides/<id>/index.tsx \
  '{method:"PUT",path:"/__notes",query:{},body:{slideId:"<id>",index:0,text:"Hello"},source:$s}' \
  | curl -s -H 'content-type: application/json' --data-binary @- http://127.0.0.1:8091/apply \
  | jq -r .source
```

## Upstream code

`upstream/` holds the editing functions fetched verbatim from the open-slide
repository at the tag matching the version `shell/package.json` pins, by
`fetch-upstream.mjs`, which names every file it takes. It is not committed;
`make sidecar` fetches it. The package does not export these functions, so
they cannot come from `node_modules`. The `LICENSE` and `VERSION` files beside
them say what was taken.
