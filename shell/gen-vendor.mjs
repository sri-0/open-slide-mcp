// Writes the vendor entries the import map points decks at. Each re-exports a
// package by explicit name: rolldown's CommonJS interop turns `export *` into
// a lone default export, and an import map needs the real names.
import { readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

process.env.NODE_ENV = 'production';
const require = createRequire(import.meta.url);

// React ships as CommonJS: require() gives its names, and module.exports is
// the default export behind `import React from 'react'`.
const cjs = {
  'react.js': 'react',
  'react-jsx-runtime.js': 'react/jsx-runtime',
  'react-dom.js': 'react-dom',
  'react-dom-client.js': 'react-dom/client',
};
// exports.json tells the deck compiler which names each specifier provides,
// so a deck importing a name that does not exist fails at compile time
// instead of in every viewer's browser.
const exportsByModule = { 'react/jsx-dev-runtime': ['Fragment', 'jsxDEV'] };
for (const [file, specifier] of Object.entries(cjs)) {
  const names = Object.keys(require(specifier));
  exportsByModule[specifier] = [...names, 'default'];
  writeFileSync(
    new URL(`vendor/${file}`, import.meta.url),
    `import * as m from ${JSON.stringify(specifier)};\nexport const { ${names.join(', ')} } = m;\nexport default m.default;\n`,
  );
}

// The runtime is ESM with no default export. It imports open-slide's virtual
// modules, so it cannot be loaded here; its names come from the export list
// of its entry file.
const names = readFileSync(fileURLToPath(import.meta.resolve('@open-slide/core')), 'utf8').match(/^export \{ (.*) \};$/m)[1];
writeFileSync(new URL('vendor/core.js', import.meta.url), `import * as m from '@open-slide/core';\nexport const { ${names} } = m;\n`);
exportsByModule['@open-slide/core'] = names.split(', ');
writeFileSync(new URL('vendor/exports.json', import.meta.url), JSON.stringify(exportsByModule, null, 1) + '\n');
