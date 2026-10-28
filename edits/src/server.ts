/**
 * Loopback HTTP front for `apply`. The Go server POSTs the browser's request
 * and the deck's source to /apply and gets the browser's reply and the new
 * source back. The process does nothing else: it runs under Node's permission
 * model with read access to dist/ alone, so a patch has no file, process or
 * network capability to reach for.
 */
import { createServer } from 'node:http';
import { apply, type ApplyRequest } from './apply.ts';

const addr = process.env.SIDECAR_ADDR ?? '127.0.0.1:8091';
// A deck source is tens of kilobytes; 4 MiB leaves room for a batch of edits
// without letting one client hold an unbounded buffer.
const maxBody = 4 * 1024 * 1024;

const colon = addr.lastIndexOf(':');
const host = addr.slice(0, colon).replace(/^\[(.*)\]$/, '$1');
const port = Number(addr.slice(colon + 1));
if (!/^127\.\d+\.\d+\.\d+$/.test(host) && host !== '::1') {
  console.error(`sidecar: SIDECAR_ADDR is ${addr}; set it to 127.0.0.1:<port> or [::1]:<port>`);
  process.exit(1);
}

const server = createServer((req, res) => {
  const started = performance.now();
  let line = `${req.method} ${req.url}`;
  const reply = (status: number, body: unknown, headers: Record<string, string> = {}) => {
    res.writeHead(status, { 'content-type': 'application/json', ...headers });
    res.end(JSON.stringify(body));
    console.error(`${line} ${status} ${(performance.now() - started).toFixed(1)}ms`);
  };
  const chunks: Buffer[] = [];
  let size = 0;
  req.on('data', (chunk: Buffer) => {
    if (res.headersSent) return;
    size += chunk.length;
    // `connection: close` makes Node flush the 413 and drop the socket rather
    // than read the rest of an oversized body.
    if (size > maxBody) return reply(413, { error: 'body too large' }, { connection: 'close' });
    chunks.push(chunk);
  });
  req.on('end', () => {
    if (res.headersSent) return;
    if (req.method === 'GET' && req.url === '/healthz') return reply(200, { ok: true });
    if (req.method !== 'POST' || req.url !== '/apply') return reply(404, { error: 'not found' });
    let input: ApplyRequest;
    try {
      input = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    } catch {
      return reply(400, { error: 'body must be JSON' });
    }
    if (
      typeof input?.method !== 'string' ||
      typeof input.path !== 'string' ||
      typeof input.source !== 'string'
    ) {
      return reply(400, { error: 'method, path and source are required' });
    }
    line += ` ${input.method} ${input.path}`;
    reply(200, apply(input));
  });
});

server.listen(port, host, () => {
  console.error(`sidecar: listening on ${addr}`);
});
