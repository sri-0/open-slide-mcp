/** Replaces node:crypto for the bundle: the host supplies globalThis.crypto.randomUUID (Node has it natively; the Go bridge installs one). */
export function randomUUID(): string {
  return (globalThis as { crypto: { randomUUID(): string } }).crypto.randomUUID();
}
