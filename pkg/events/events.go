// Package events fans out per-deck change notifications to the browsers that
// have that deck open, as server-sent events. It stands in for the Vite HMR
// socket the editor used to listen on: the shell's hot shim turns each event
// into the same in-app event name, so the editor re-imports a recompiled deck
// without a reload.
package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Broker holds the open streams, grouped by deck.
type Broker struct {
	mu   sync.Mutex // guards subs
	subs map[string]map[chan message]struct{}
}

type message struct {
	name string
	data []byte
}

// New returns an empty broker.
func New() *Broker {
	return &Broker{subs: map[string]map[chan message]struct{}{}}
}

// Publish sends one named event, with data as JSON, to every stream open on
// deckID. A slow stream drops the event rather than blocking the publisher.
func (b *Broker) Publish(deckID, name string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[deckID] {
		select {
		case ch <- message{name, body}:
		default:
		}
	}
}

// Serve streams deckID's events to w until the request ends. A comment line
// every 25 seconds keeps proxies from closing an idle stream.
func (b *Broker) Serve(deckID string, w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan message, 16)
	b.mu.Lock()
	if b.subs[deckID] == nil {
		b.subs[deckID] = map[chan message]struct{}{}
	}
	b.subs[deckID][ch] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs[deckID], ch)
		if len(b.subs[deckID]) == 0 {
			delete(b.subs, deckID)
		}
		b.mu.Unlock()
	}()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
		case m := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.name, m.data)
		}
		fl.Flush()
	}
}
