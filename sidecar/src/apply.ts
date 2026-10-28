/**
 * The route table the open-slide editor talks to, applied to a source string
 * instead of a file. Each branch mirrors the dev-server handler it replaces
 * (upstream/vite/routes/edit.ts, comments.ts, slides.ts,
 * design-plugin.ts and notes-plugin.ts): the same checks in the same order,
 * the same status codes, the same bodies. The file read, the file write and
 * the request-header guard (origin, content-type) belong to the Go server, so
 * "slide not found" and the 403/415 replies never come from here; nor do the
 * slide-directory routes (POST /__slides/:id/duplicate, DELETE /__slides/:id),
 * which touch the disk rather than the source.
 */
import { defaultDesign, type DesignSystem } from '../upstream/app/lib/design.ts';
import { applyEditBatch, type BatchEdit } from '../upstream/editing/batch-edit.ts';
import {
  b64urlEncode,
  buildMarker,
  type CommentPayload,
  findInsertion,
  newCommentId,
  offsetToLine,
  parseCommentPage,
  parseCommentRect,
  parseMarkers,
  removeMarker,
  resolveTargets,
  updateMarkerNote,
} from '../upstream/editing/comments.ts';
import { applyEdit, type EditOp } from '../upstream/editing/edit-ops.ts';
import { applyRevertAsset } from '../upstream/editing/revert-asset.ts';
import {
  duplicateNotesElementInSource,
  duplicatePageInDefaultExportInSource,
  removeNotesElementInSource,
  removePageFromDefaultExportInSource,
  reorderDefaultExportPagesInSource,
  reorderNotesArrayInSource,
  SLIDE_ID_RE,
  updateMetaTitleInSource,
  validateSlideName,
} from '../upstream/editing/slide-ops.ts';
import {
  applyDesignWrite,
  mergeDesign,
  parseSlideDesign,
} from '../upstream/vite/design-plugin.ts';
import { applyNotesEdit } from '../upstream/vite/notes-plugin.ts';

/** What the Go server forwards: the browser's request and the deck's current index.tsx. */
export type ApplyRequest = {
  method: string;
  path: string;
  query?: Record<string, string>;
  body?: unknown;
  source: string;
};

/** The reply the browser expects, plus the new index.tsx only when the route changed it. */
export type ApplyResponse = { status: number; body: unknown; source?: string };

const notFound: ApplyResponse = { status: 404, body: { error: 'not found' } };
// Upstream resolves the id to slides/<id>/index.tsx; with this character set
// the path can never escape the slides root, so the regex is the whole check.
const invalidSlideId: ApplyResponse = { status: 400, body: { error: 'invalid slideId' } };

/** A 200 that carries `source` only when the rewrite changed something, so Go knows whether to write. */
function done(source: string, next: string, body: unknown): ApplyResponse {
  return next === source ? { status: 200, body } : { status: 200, body, source: next };
}

/** Dispatch on the mount prefix the way Vite's connect middleware did: `/__edit/batch` → `/batch`. */
export function apply(req: ApplyRequest): ApplyResponse {
  const [, mount, rest = '/'] = req.path.match(/^(\/__[a-z]+)(\/.*)?$/) ?? [];
  const query = req.query ?? {};
  const body = req.body ?? {};
  try {
    switch (mount) {
      case '/__edit':
        return edit(req.method, rest, body, req.source);
      case '/__comments':
        return comments(req.method, rest, query, body, req.source);
      case '/__design':
        return design(req.method, rest, query, body, req.source);
      case '/__notes':
        return notes(req.method, rest, body, req.source);
      case '/__slides':
        return slides(req.method, rest, body, req.source);
      default:
        return notFound;
    }
  } catch (err) {
    return { status: 500, body: { error: String((err as Error).message ?? err) } };
  }
}

type EditBody = { slideId?: string; line?: number; column?: number; ops?: EditOp[] };
type EditBatchBody = { slideId?: string; edits?: BatchEdit[] };

function edit(method: string, rest: string, raw: unknown, source: string): ApplyResponse {
  if (method !== 'POST') return notFound;
  if (rest === '/') {
    const body = raw as EditBody;
    if (!SLIDE_ID_RE.test(body.slideId ?? '')) return invalidSlideId;
    if (!body.line || body.line < 1) return { status: 400, body: { error: 'invalid line' } };
    if (!Array.isArray(body.ops)) return { status: 400, body: { error: 'missing ops' } };
    const result = applyEdit(source, body.line, body.column ?? 0, body.ops);
    if (!result.ok) return { status: result.status, body: { error: result.error } };
    return done(source, result.source, { ok: true, changed: result.source !== source });
  }
  if (rest === '/revert-asset') {
    const body = raw as { slideId?: string; assetPath?: string };
    if (!SLIDE_ID_RE.test(body.slideId ?? '')) return invalidSlideId;
    const assetPath = body.assetPath;
    if (typeof assetPath !== 'string' || !assetPath) {
      return { status: 400, body: { error: 'missing assetPath' } };
    }
    if (!assetPath.startsWith('./assets/') && !assetPath.startsWith('@assets/')) {
      return { status: 400, body: { error: 'asset path must start with ./assets/ or @assets/' } };
    }
    const result = applyRevertAsset(source, assetPath);
    if (!result.ok) return { status: result.status, body: { error: result.error } };
    return done(source, result.source, { ok: true, changed: result.source !== source });
  }
  if (rest === '/batch') {
    const body = raw as EditBatchBody;
    if (!SLIDE_ID_RE.test(body.slideId ?? '')) return invalidSlideId;
    if (!Array.isArray(body.edits)) return { status: 400, body: { error: 'missing edits' } };
    const { source: next, results } = applyEditBatch(source, body.edits);
    return done(source, next, { ok: true, changed: next !== source, results });
  }
  return notFound;
}

type AddCommentBody = {
  slideId?: string;
  line?: number;
  column?: number;
  text?: string;
  hint?: string;
  rect?: unknown;
  page?: unknown;
};

function comments(
  method: string,
  rest: string,
  query: Record<string, string>,
  raw: unknown,
  source: string,
): ApplyResponse {
  if (method === 'GET' && rest === '/') {
    if (!SLIDE_ID_RE.test(query.slideId ?? '')) return invalidSlideId;
    return { status: 200, body: { comments: resolveTargets(source, parseMarkers(source)) } };
  }
  if (method === 'POST' && rest === '/add') {
    const body = raw as AddCommentBody;
    if (!SLIDE_ID_RE.test(body.slideId ?? '')) return invalidSlideId;
    if (!body.line || body.line < 1) return { status: 400, body: { error: 'invalid line' } };
    if (!body.text || typeof body.text !== 'string') {
      return { status: 400, body: { error: 'missing text' } };
    }
    const plan = findInsertion(source, body.line, body.column);
    if (!plan) {
      return {
        status: 422,
        body: {
          error:
            'could not find a JSX container around line ' +
            `${body.line}. Try clicking a different element.`,
        },
      };
    }
    const id = newCommentId();
    const ts = new Date().toISOString();
    const payload: CommentPayload = {
      note: body.text,
      hint: body.hint,
      rect: parseCommentRect(body.rect),
      page: parseCommentPage(body.page),
    };
    const marker = buildMarker(plan, id, ts, b64urlEncode(JSON.stringify(payload)));
    const next = source.slice(0, plan.offset) + marker + source.slice(plan.offset);
    return done(source, next, { id, line: offsetToLine(next, plan.offset + 1) });
  }
  const id = rest.slice(1);
  if (method === 'PUT') {
    if (!/^c-[a-f0-9]+$/.test(id)) return { status: 400, body: { error: 'invalid id' } };
    const body = raw as { slideId?: string; text?: string };
    if (!SLIDE_ID_RE.test(body.slideId ?? '')) return invalidSlideId;
    if (!body.text || typeof body.text !== 'string') {
      return { status: 400, body: { error: 'missing text' } };
    }
    const next = updateMarkerNote(source, id, body.text);
    if (next === null) return { status: 404, body: { error: 'marker not found' } };
    return done(source, next, { ok: true });
  }
  if (method === 'DELETE') {
    if (!/^c-[a-f0-9]+$/.test(id)) return { status: 400, body: { error: 'invalid id' } };
    if (!SLIDE_ID_RE.test(query.slideId ?? '')) return invalidSlideId;
    const next = removeMarker(source, id);
    if (next === null) return { status: 404, body: { error: 'marker not found' } };
    return done(source, next, { ok: true });
  }
  return notFound;
}

function design(
  method: string,
  rest: string,
  query: Record<string, string>,
  raw: unknown,
  source: string,
): ApplyResponse {
  if (!SLIDE_ID_RE.test(query.slideId ?? '')) return invalidSlideId;
  if (method === 'GET' && rest === '/') {
    const parsed = parseSlideDesign(source);
    if (parsed.ok) {
      return { status: 200, body: { design: parsed.design, exists: true, warning: null } };
    }
    if (parsed.exists === false) {
      return { status: 200, body: { design: defaultDesign, exists: false, warning: null } };
    }
    return { status: 200, body: { design: defaultDesign, exists: true, warning: parsed.error } };
  }
  if (method === 'PUT' && rest === '/') {
    const patch = (raw as { patch?: Partial<DesignSystem> }).patch;
    if (!patch || typeof patch !== 'object') {
      return { status: 400, body: { error: 'missing patch object' } };
    }
    const parsed = parseSlideDesign(source);
    const baseDesign = parsed.ok ? parsed.design : defaultDesign;
    if (!parsed.ok && parsed.exists) return { status: 422, body: { error: parsed.error } };
    const merged = mergeDesign(baseDesign, patch);
    const written = applyDesignWrite(source, merged);
    if (!written.ok) return { status: written.status, body: { error: written.error } };
    return done(source, written.source, { ok: true, design: merged, created: written.created });
  }
  if (method === 'POST' && rest === '/reset') {
    const written = applyDesignWrite(source, defaultDesign);
    if (!written.ok) return { status: written.status, body: { error: written.error } };
    return done(source, written.source, {
      ok: true,
      design: defaultDesign,
      created: written.created,
    });
  }
  return notFound;
}

function notes(method: string, rest: string, raw: unknown, source: string): ApplyResponse {
  if (method !== 'PUT' || rest !== '/') return notFound;
  const body = raw as { slideId?: string; index?: number; text?: string };
  if (!SLIDE_ID_RE.test(body.slideId ?? '')) return invalidSlideId;
  if (typeof body.index !== 'number') return { status: 400, body: { error: 'missing index' } };
  if (typeof body.text !== 'string') return { status: 400, body: { error: 'missing text' } };
  const result = applyNotesEdit(source, body.index, body.text);
  if (!result.ok) return { status: result.status, body: { error: result.error } };
  return done(source, result.source, { ok: true, changed: result.source !== source });
}

function slides(method: string, rest: string, raw: unknown, source: string): ApplyResponse {
  const reorderMatch = rest.match(/^\/([^/]+)\/reorder$/);
  if (reorderMatch && method === 'PUT') {
    const slideId = reorderMatch[1];
    if (!SLIDE_ID_RE.test(slideId)) return invalidSlideId;
    const { order } = raw as { order?: unknown };
    if (!Array.isArray(order) || !order.every((v) => Number.isInteger(v))) {
      return { status: 400, body: { error: 'invalid order' } };
    }
    const reordered = reorderDefaultExportPagesInSource(source, order as number[]);
    if (reordered === null) {
      return {
        status: 422,
        body: { error: 'could not reorder pages — order must be a permutation of the existing array' },
      };
    }
    const withNotes = reorderNotesArrayInSource(reordered, order as number[]);
    if (withNotes === null) {
      return {
        status: 422,
        body: { error: 'could not reorder pages — `notes` export has an unexpected shape' },
      };
    }
    return done(source, withNotes, { ok: true, slideId, order });
  }

  const pageOpMatch = rest.match(/^\/([^/]+)\/pages\/(\d+)(?:\/([a-z]+))?$/);
  if (pageOpMatch) {
    const [, slideId, page, op] = pageOpMatch;
    const pageIndex = Number.parseInt(page, 10);
    if (!SLIDE_ID_RE.test(slideId)) return invalidSlideId;
    if (!Number.isInteger(pageIndex) || pageIndex < 0) {
      return { status: 400, body: { error: 'invalid page index' } };
    }
    const isDelete = method === 'DELETE' && !op;
    const isDuplicate = method === 'POST' && op === 'duplicate';
    if (!isDelete && !isDuplicate) return notFound;
    const updated = isDelete
      ? removePageFromDefaultExportInSource(source, pageIndex)
      : duplicatePageInDefaultExportInSource(source, pageIndex);
    if (updated === null) {
      return {
        status: 422,
        body: {
          error: isDelete
            ? 'could not delete page — index out of range or default export is not an array'
            : 'could not duplicate page — index out of range or default export is not an array',
        },
      };
    }
    const withNotes = isDelete
      ? removeNotesElementInSource(updated, pageIndex)
      : duplicateNotesElementInSource(updated, pageIndex);
    if (withNotes === null) {
      return {
        status: 422,
        body: {
          error: isDelete
            ? 'could not delete page — `notes` export has an unexpected shape'
            : 'could not duplicate page — `notes` export has an unexpected shape',
        },
      };
    }
    return done(source, withNotes, { ok: true, slideId, index: pageIndex });
  }

  const idMatch = rest.match(/^\/([^/]+)$/);
  if (!idMatch) return notFound;
  const slideId = idMatch[1];
  if (!SLIDE_ID_RE.test(slideId)) return invalidSlideId;
  if (method !== 'PATCH') return notFound;
  const name = validateSlideName((raw as { name?: unknown }).name);
  if (!name) return { status: 400, body: { error: 'invalid name' } };
  const updated = updateMetaTitleInSource(source, name);
  if (updated === null) {
    return {
      status: 422,
      body: { error: 'could not locate a safe place to write meta.title in index.tsx' },
    };
  }
  return done(source, updated, { ok: true, slideId, name });
}
