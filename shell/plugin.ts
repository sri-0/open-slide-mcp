import { readFileSync } from 'node:fs';
import type { Plugin } from 'vite';
// The package's exports map hides package.json, so it is read through
// node_modules.
import core from './node_modules/@open-slide/core/package.json' with { type: 'json' };

// Chunk name → bare specifier a deck may import. The import map in index.html
// points each specifier at its chunk, so a deck compiled outside this build
// shares the shell's React and open-slide runtime instances.
export const vendor: Record<string, string> = {
  'vendor-react': 'react',
  'vendor-react-jsx-runtime': 'react/jsx-runtime',
  'vendor-react-jsx-dev-runtime': 'react/jsx-dev-runtime',
  'vendor-react-dom': 'react-dom',
  'vendor-react-dom-client': 'react-dom/client',
  'vendor-core': '@open-slide/core',
};

const prefix = 'virtual:open-slide/';
const resolved = '\0shell:';

// open-slide's own plugin generates these four modules from the workspace at
// build time. The shell serves them instead: decks are discovered at runtime
// from /manifest.json, the gallery is off (which also removes the editor's
// back-to-home links), and there are no folders or themes.
const modules: Record<string, string> = {
  config: `export default ${JSON.stringify({
    base: '/',
    build: { showSlideBrowser: false, showSlideUi: true, allowHtmlDownload: true },
    version: core.version,
  })};\n`,
  slides: `const manifest = await (await fetch(globalThis.__osBase + 'manifest.json', { credentials: 'same-origin' })).json();
export const slideIds = manifest.slides.map((s) => s.id);
export const slideThemes = Object.fromEntries(manifest.slides.filter((s) => s.theme).map((s) => [s.id, s.theme]));
export const slideCreatedAt = Object.fromEntries(manifest.slides.map((s) => [s.id, Date.parse(s.createdAt)]));
const slideUrls = Object.fromEntries(manifest.slides.map((s) => [s.id, s.url]));
const slideImportTokens = Object.fromEntries(manifest.slides.map((s) => [s.id, 0]));
if (import.meta.hot) {
  import.meta.hot.on('open-slide:slide-changed', (data) => {
    const ids = Array.isArray(data?.slideIds) ? data.slideIds : [];
    const token = Date.now();
    for (const id of ids) {
      if (Object.prototype.hasOwnProperty.call(slideImportTokens, id)) slideImportTokens[id] = token;
    }
  });
}
export async function loadSlide(id) {
  if (!Object.prototype.hasOwnProperty.call(slideUrls, id)) throw new Error('Slide not found: ' + id);
  return import(/* @vite-ignore */ slideUrls[id] + '?t=' + slideImportTokens[id]);
}
`,
  folders: `export default { folders: [], assignments: {} };\n`,
  themes: `export const themes = [];
export async function loadThemeDemo(id) { throw new Error('Theme demo not found: ' + id); }
`,
};

export function shellPlugin(): Plugin {
  return {
    name: 'shell',
    enforce: 'pre',
    resolveId(id) {
      return id.startsWith(prefix) ? resolved + id.slice(prefix.length) : null;
    },
    load(id) {
      return id.startsWith(resolved) ? modules[id.slice(resolved.length)] : null;
    },
    // The editor calls its server with absolute paths (`fetch('/__edit')`);
    // under a shared origin those must hang off the deck's base.
    transform(code, id) {
      if (!id.includes('/@open-slide/core/src/app/') || !code.includes("'/__") && !code.includes('`/__')) return null;
      return code.replaceAll("fetch('/__", "fetch(globalThis.__osBase + '__").replaceAll('fetch(`/__', 'fetch(`${globalThis.__osBase}__');
    },
    // The names each vendor entry exports, for the deck compiler.
    generateBundle() {
      this.emitFile({ type: 'asset', fileName: 'exports.json', source: readFileSync(new URL('./vendor/exports.json', import.meta.url)) });
    },
    transformIndexHtml: {
      order: 'post',
      handler(_html, ctx) {
        const entries = Object.values(ctx.bundle!).filter((c) => c.type === 'chunk' && c.isEntry);
        const imports = Object.fromEntries(
          Object.entries(vendor).map(([name, specifier]) => [specifier, '/' + entries.find((c) => c.name === name)!.fileName]),
        );
        const hot = entries.find((c) => c.name === 'hot')!;
        // The import map must precede every module script, and the hot shim
        // must run before the app's module graph evaluates.
        return [
          { tag: 'script', attrs: { type: 'importmap' }, children: JSON.stringify({ imports }), injectTo: 'head-prepend' },
          { tag: 'script', attrs: { type: 'module', src: '/' + hot.fileName }, injectTo: 'head-prepend' },
        ];
      },
    },
  };
}
