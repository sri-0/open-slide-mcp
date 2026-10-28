// Stands in for Vite's `import.meta.hot`, which the shell build defines as
// `globalThis.__osHot`. The editor's HMR listeners attach here and events
// arrive on a server-sent event stream instead of Vite's WebSocket.
export {};

declare global {
  // The deck's base path, set below before the app's module graph evaluates.
  var __osBase: string;
  var __osHot: {
    on(name: string, fn: (data: unknown) => void): void;
    off(name: string, fn: (data: unknown) => void): void;
    send(name: string, data: unknown): void;
    emit(name: string, data?: unknown): void;
  };
}

const listeners = new Map<string, Set<(data: unknown) => void>>();
// The server names the deck's base in a meta tag: "/" when each deck has its
// own origin, "/<deck>/" when decks share one. Every absolute URL the app
// and this shim build hangs off it.
globalThis.__osBase = document.querySelector('meta[name="osmcp-base"]')?.getAttribute('content') ?? '/';
const events = new EventSource(globalThis.__osBase + '__events');

function emit(name: string, data?: unknown) {
  listeners.get(name)?.forEach((fn) => fn(data));
}

globalThis.__osHot = {
  on(name, fn) {
    let set = listeners.get(name);
    if (!set) {
      // EventSource only delivers named events to a listener for that name,
      // so each name the editor listens to is subscribed on first use.
      set = new Set();
      listeners.set(name, set);
      events.addEventListener(name, (e) => emit(name, JSON.parse(e.data)));
    }
    set.add(fn);
  },
  off(name, fn) {
    listeners.get(name)?.delete(fn);
  },
  emit,
  // Only the current-slide cursor has a receiver; nothing else the editor
  // sends is handled outside Vite's dev server.
  send(name, data) {
    if (name !== 'open-slide:current') return;
    void fetch(globalThis.__osBase + '__current', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(data),
      credentials: 'same-origin',
    });
  },
};

// The "agent connected" badge follows the stream.
events.onopen = () => emit('vite:ws:connect');
events.onerror = () => emit('vite:ws:disconnect');
