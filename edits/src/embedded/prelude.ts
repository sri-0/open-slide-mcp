/**
 * Buffer for the two calls upstream/editing/comments.ts makes:
 * Buffer.from(s,'utf8').toString('base64') and Buffer.from(s,'base64').toString('utf8').
 * Installed only where Buffer is absent (goja); Node keeps its own.
 */
declare const globalThis: { Buffer?: unknown } & Record<string, unknown>;

if (typeof globalThis.Buffer === 'undefined') {
  const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  const LOOKUP: Record<string, number> = {};
  for (let i = 0; i < 64; i++) LOOKUP[ALPHABET[i]] = i;
  LOOKUP['-'] = 62;
  LOOKUP['_'] = 63;

  function utf8Encode(s: string): number[] {
    const out: number[] = [];
    for (let i = 0; i < s.length; i++) {
      let c = s.charCodeAt(i);
      if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
        const d = s.charCodeAt(i + 1);
        if (d >= 0xdc00 && d <= 0xdfff) {
          c = 0x10000 + ((c - 0xd800) << 10) + (d - 0xdc00);
          i++;
        }
      }
      if (c >= 0xd800 && c <= 0xdfff) c = 0xfffd;
      if (c < 0x80) out.push(c);
      else if (c < 0x800) out.push(0xc0 | (c >> 6), 0x80 | (c & 63));
      else if (c < 0x10000) out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
      else
        out.push(
          0xf0 | (c >> 18),
          0x80 | ((c >> 12) & 63),
          0x80 | ((c >> 6) & 63),
          0x80 | (c & 63),
        );
    }
    return out;
  }

  function utf8Decode(b: number[]): string {
    let s = '';
    for (let i = 0; i < b.length; ) {
      const c = b[i];
      let cp = -1;
      let n = 0;
      if (c < 0x80) {
        cp = c;
        n = 1;
      } else if (c >= 0xc2 && c <= 0xdf) {
        n = 2;
      } else if (c >= 0xe0 && c <= 0xef) {
        n = 3;
      } else if (c >= 0xf0 && c <= 0xf4) {
        n = 4;
      }
      if (n > 1) {
        cp = c & (n === 2 ? 0x1f : n === 3 ? 0x0f : 0x07);
        let ok = i + n <= b.length;
        for (let k = 1; ok && k < n; k++) {
          const d = b[i + k];
          if ((d & 0xc0) !== 0x80) ok = false;
          else cp = (cp << 6) | (d & 0x3f);
        }
        if (!ok || (n === 3 && (cp < 0x800 || (cp >= 0xd800 && cp <= 0xdfff))) || (n === 4 && (cp < 0x10000 || cp > 0x10ffff))) {
          cp = -1;
        }
      }
      if (cp < 0) {
        s += '�';
        i += 1;
        continue;
      }
      s += String.fromCodePoint(cp);
      i += n;
    }
    return s;
  }

  function b64Encode(b: number[]): string {
    let s = '';
    for (let i = 0; i < b.length; i += 3) {
      const x = (b[i] << 16) | ((b[i + 1] ?? 0) << 8) | (b[i + 2] ?? 0);
      s += ALPHABET[(x >> 18) & 63] + ALPHABET[(x >> 12) & 63];
      s += i + 1 < b.length ? ALPHABET[(x >> 6) & 63] : '=';
      s += i + 2 < b.length ? ALPHABET[x & 63] : '=';
    }
    return s;
  }

  function b64Decode(s: string): number[] {
    const out: number[] = [];
    let acc = 0;
    let bits = 0;
    for (let i = 0; i < s.length; i++) {
      const ch = s[i];
      if (ch === '=') break;
      const v = LOOKUP[ch];
      if (v === undefined) continue;
      acc = (acc << 6) | v;
      bits += 6;
      if (bits >= 8) {
        bits -= 8;
        out.push((acc >> bits) & 0xff);
      }
    }
    return out;
  }

  class Buf {
    constructor(private bytes: number[]) {}
    toString(enc?: string): string {
      return enc === 'base64' ? b64Encode(this.bytes) : utf8Decode(this.bytes);
    }
  }

  globalThis.Buffer = {
    from(s: string, enc?: string): Buf {
      return new Buf(enc === 'base64' ? b64Decode(s) : utf8Encode(s));
    },
  };
}
