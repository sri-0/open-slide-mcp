/** Replaces node:fs/promises and node:path: nothing apply() reaches should touch them; any access throws so a leak is loud. */
const absent = new Proxy(
  {},
  {
    get(_t, key) {
      throw new Error(`node builtin not available in this bundle: ${String(key)}`);
    },
  },
);
export default absent;
