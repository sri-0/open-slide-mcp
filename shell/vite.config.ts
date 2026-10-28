import { createViteConfig } from '@open-slide/core/vite';
import path from 'node:path';
import { defineConfig, mergeConfig } from 'vite';
import { shellPlugin, vendor } from './plugin.ts';

const here = import.meta.dirname;

export default defineConfig(async () =>
  mergeConfig(
    // open-slide globs this workspace for slides and themes at build time; it
    // stays empty because the shell discovers decks at runtime.
    await createViteConfig({ userCwd: path.join(here, 'empty-workspace'), mode: 'build' }),
    {
      base: '/',
      // The editor only edits when it believes it runs under `vite dev`: DEV
      // gates the inspector, PROD hides it, and `import.meta.hot` is the
      // channel its listeners attach to. Vite's `mode` leaves the editor out,
      // and NODE_ENV=development would pull React's development build.
      define: {
        'import.meta.env.DEV': 'true',
        'import.meta.env.PROD': 'false',
        'import.meta.hot': 'globalThis.__osHot',
        // The router's basename and the presenter URL follow the deck's base.
        'import.meta.env.BASE_URL': 'globalThis.__osBase',
      },
      plugins: [shellPlugin()],
      build: {
        outDir: path.join(here, 'dist'),
        manifest: true,
        minify: true,
        rolldownOptions: {
          input: {
            index: 'index.html',
            hot: path.join(here, 'src/hot.ts'),
            ...Object.fromEntries(
              Object.keys(vendor).map((name) => [name, path.join(here, `vendor/${name.slice('vendor-'.length)}.js`)]),
            ),
          },
          // Vendor entries keep their named exports so the import map can
          // hand them to decks.
          preserveEntrySignatures: 'strict',
        },
      },
    },
  ),
);
