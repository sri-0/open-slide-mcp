package workspace

import "time"

// Current is the live cursor one deck's editor last reported: which page and
// element the user is looking at. It is kept per deck, in memory, so one
// thread's agent only ever sees cursors for decks it can name.
type Current struct {
	DeckID    string         `json:"deck_id"`
	UpdatedAt time.Time      `json:"updated_at"`
	Data      map[string]any `json:"data"`
}

// SetCurrent records the cursor the browser posted for a deck.
func (s *Store) SetCurrent(id string, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.decks[id]; !ok {
		return ErrNotFound
	}
	s.current[id] = Current{DeckID: id, UpdatedAt: time.Now().UTC(), Data: data}
	return nil
}

// ReadCurrent returns the newest cursor among the given decks, or nil when
// none of them has reported one.
func (s *Store) ReadCurrent(ids ...string) *Current {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out *Current
	for _, id := range ids {
		if c, ok := s.current[id]; ok && (out == nil || c.UpdatedAt.After(out.UpdatedAt)) {
			out = &c
		}
	}
	return out
}
