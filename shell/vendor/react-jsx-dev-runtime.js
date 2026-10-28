// The jsx-dev-runtime decks compile against. The Go compiler runs esbuild
// with JSXDev on, which passes each element's source position; React's
// production build has no jsxDEV, so this one wraps jsx/jsxs and tags host
// elements with the `data-slide-loc` the inspector selects by. esbuild's
// column is 1-based at the element's `<`; the inspector expects Babel's
// 0-based column.
import { ImagePlaceholder } from '@open-slide/core';
import { jsx, jsxs } from 'react/jsx-runtime';

export { Fragment } from 'react/jsx-runtime';

export function jsxDEV(type, props, key, isStaticChildren, source) {
  if (
    source &&
    (typeof type === 'string' || type === ImagePlaceholder) &&
    !('data-slide-loc' in props)
  ) {
    props['data-slide-loc'] = source.lineNumber + ':' + (source.columnNumber - 1);
  }
  return (isStaticChildren ? jsxs : jsx)(type, props, key);
}
