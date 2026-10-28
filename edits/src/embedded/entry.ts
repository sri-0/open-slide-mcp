// The entry of the embedded bundle: the same apply() the sidecar serves,
// exposed as a global that takes and returns JSON strings, which is all the
// Go runtime passes across. The prelude supplies the Buffer the upstream
// comment code expects.
import './prelude.ts';
import { apply } from '../apply.ts';

(globalThis as Record<string, unknown>).apply = (json: string) => JSON.stringify(apply(JSON.parse(json)));
