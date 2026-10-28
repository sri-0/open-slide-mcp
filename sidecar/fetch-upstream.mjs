// Fetches the open-slide editing sources the sidecar bundles, at the exact
// version the shell pins, into upstream/ (not committed). The package does
// not export these functions, so they come from the repository tag. A file
// that moves upstream fails here, loudly, before the bundle is built.
import { execFileSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

const here = import.meta.dirname;
const version = JSON.parse(readFileSync(path.join(here, '../shell/package.json'), 'utf8')).dependencies['@open-slide/core'];
const tag = `@open-slide/core@${version}`;
// Relative to packages/core/src: the editing functions and what they import.
const files = [
  'editing/babel-walk.ts',
  'editing/batch-edit.ts',
  'editing/comments.ts',
  'editing/edit-ops.ts',
  'editing/revert-asset.ts',
  'editing/slide-ops.ts',
  'app/lib/design.ts',
  'app/lib/text-diff.ts',
  'files/folders.ts',
  'files/short-id.ts',
  'http/request-guard.ts',
  'vite/design-plugin.ts',
  'vite/notes-plugin.ts',
  'vite/recent-writes.ts',
  'vite/routes/context.ts',
];

const url = `https://github.com/open-slide/open-slide/archive/refs/tags/${tag}.tar.gz`;
const res = await fetch(url);
if (!res.ok) throw new Error(`${url}: ${res.status}`);
const tmp = mkdtempSync(path.join(tmpdir(), 'open-slide-'));
const archive = path.join(tmp, 'src.tgz');
writeFileSync(archive, Buffer.from(await res.arrayBuffer()));
execFileSync('tar', ['-xzf', archive, '-C', tmp, '--strip-components=1']);
const out = path.join(here, 'upstream');
rmSync(out, { recursive: true, force: true });
for (const f of files) {
  const src = path.join(tmp, 'packages/core/src', f);
  if (!existsSync(src)) throw new Error(`${tag} has no packages/core/src/${f}; update the list in fetch-upstream.mjs`);
  mkdirSync(path.dirname(path.join(out, f)), { recursive: true });
  cpSync(src, path.join(out, f));
}
const licence = ['packages/core/LICENSE', 'LICENSE'].map((p) => path.join(tmp, p)).find(existsSync);
if (!licence) throw new Error(`${tag} has no LICENSE`);
cpSync(licence, path.join(out, 'LICENSE'));
writeFileSync(path.join(out, 'VERSION'), `${tag}\n`);
rmSync(tmp, { recursive: true, force: true });
console.log(`fetched ${files.length} files from ${tag} into sidecar/upstream`);
