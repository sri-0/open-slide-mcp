// Package workspace manages decks inside an open-slide workspace: one folder
// per deck under slides/, plus a metadata index that ties each deck to the
// thread (or artifact) that owns it.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Deck is the metadata record for one slide folder.
type Deck struct {
	ID         string    `json:"id"`
	ThreadID   string    `json:"thread_id"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastAccess time.Time `json:"last_access"`
}

// Comment is one @slide-comment marker left by the inspector.
type Comment struct {
	ID   string `json:"id"`
	TS   string `json:"ts"`
	Text string `json:"text"` // base64url(JSON) exactly as written in the source
	Line int    `json:"line"`
}

var (
	ErrNotFound  = errors.New("deck not found")
	ErrConflict  = errors.New("deck changed since it was last read; call deck_read and merge")
	ErrExists    = errors.New("deck already exists")
	ErrBadThread = errors.New("thread_id is required")
	ErrBadName   = errors.New("name must be kebab-case: lowercase letters, digits and dashes")
)

var (
	slugRe    = regexp.MustCompile(`[^a-z0-9]+`)
	nameRe    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	commentRe = regexp.MustCompile(`\{/\*\s*@slide-comment\s+id="(c-[a-f0-9]+)"\s+ts="([^"]+)"\s+text="([A-Za-z0-9_\-]+={0,2})"\s*\*/\}`)
)

// Store owns the slides directory and the metadata index.
type Store struct {
	root     string // workspace dir
	slides   string // <root>/slides
	metaPath string // <root>/.open-slide-mcp/decks.json
	mu       sync.Mutex
	decks    map[string]*Deck
	now      func() time.Time
}

// Open loads (or creates) the index for the given workspace.
func Open(root string) (*Store, error) {
	s := &Store{
		root:     root,
		slides:   filepath.Join(root, "slides"),
		metaPath: filepath.Join(root, ".open-slide-mcp", "decks.json"),
		decks:    map[string]*Deck{},
		now:      time.Now,
	}
	if err := os.MkdirAll(s.slides, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.metaPath), 0o755); err != nil {
		return nil, err
	}
	// open-slide expects this manifest to exist; a mounted volume may hide the baked one.
	manifest := filepath.Join(s.slides, ".folders.json")
	if _, err := os.Stat(manifest); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(manifest, []byte("{\n  \"folders\": [],\n  \"assignments\": {}\n}\n"), 0o644); err != nil {
			return nil, err
		}
	}
	b, err := os.ReadFile(s.metaPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var list []*Deck
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.metaPath, err)
		}
		for _, d := range list {
			if _, err := os.Stat(s.indexPath(d.ID)); err == nil {
				s.decks[d.ID] = d
			}
		}
	}
	return s, s.saveLocked()
}

// Root returns the workspace directory.
func (s *Store) Root() string { return s.root }

// IndexPath returns the absolute path of a deck's index.tsx.
func (s *Store) IndexPath(id string) string { return s.indexPath(id) }

func (s *Store) indexPath(id string) string {
	return filepath.Join(s.slides, id, "index.tsx")
}

// DeckID derives the folder name from the owning thread and a deck name.
func DeckID(threadID, name string) (string, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return "", ErrBadThread
	}
	if !nameRe.MatchString(name) {
		return "", ErrBadName
	}
	t := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(threadID), "-"), "-")
	if len(t) > 24 {
		t = t[:24]
	}
	sum := sha256.Sum256([]byte(threadID))
	return fmt.Sprintf("%s-%s-%s", strings.Trim(t, "-"), hex.EncodeToString(sum[:])[:6], name), nil
}

// Slugify turns a title into a deck name usable with DeckID.
func Slugify(title string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "deck"
	}
	return s
}

// Create writes a new deck folder with the given TSX.
func (s *Store) Create(threadID, name, title, tsx string) (*Deck, string, error) {
	id, err := DeckID(threadID, name)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.decks[id]; ok {
		return nil, "", ErrExists
	}
	if _, err := os.Stat(filepath.Join(s.slides, id)); err == nil {
		return nil, "", ErrExists
	}
	if err := os.MkdirAll(filepath.Join(s.slides, id), 0o755); err != nil {
		return nil, "", err
	}
	if err := writeAtomic(s.indexPath(id), []byte(tsx)); err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	d := &Deck{ID: id, ThreadID: strings.TrimSpace(threadID), Title: title, CreatedAt: now, UpdatedAt: now, LastAccess: now}
	s.decks[id] = d
	return clone(d), Hash(tsx), s.saveLocked()
}

// Read returns the current source, its hash, and any inspector comments.
func (s *Store) Read(id string) (*Deck, string, string, []Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decks[id]
	if !ok {
		return nil, "", "", nil, ErrNotFound
	}
	b, err := os.ReadFile(s.indexPath(id))
	if err != nil {
		return nil, "", "", nil, err
	}
	d.LastAccess = s.now().UTC()
	_ = s.saveLocked()
	src := string(b)
	return clone(d), src, Hash(src), ParseComments(src), nil
}

// Write replaces the source. If expectedHash is non-empty it must match the
// hash of the current file, which protects edits made in the browser editor.
func (s *Store) Write(id, tsx, expectedHash string) (*Deck, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decks[id]
	if !ok {
		return nil, "", ErrNotFound
	}
	if expectedHash != "" {
		cur, err := os.ReadFile(s.indexPath(id))
		if err != nil {
			return nil, "", err
		}
		if Hash(string(cur)) != expectedHash {
			return nil, "", ErrConflict
		}
	}
	if err := writeAtomic(s.indexPath(id), []byte(tsx)); err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	d.UpdatedAt, d.LastAccess = now, now
	return clone(d), Hash(tsx), s.saveLocked()
}

// Get returns metadata for one deck.
func (s *Store) Get(id string) (*Deck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decks[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(d), nil
}

// Touch records activity, for example a preview request.
func (s *Store) Touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.decks[id]; ok {
		d.LastAccess = s.now().UTC()
		_ = s.saveLocked()
	}
}

// List returns decks for a thread, or every deck when threadID is empty.
func (s *Store) List(threadID string) []*Deck {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Deck
	for _, d := range s.decks {
		if threadID == "" || d.ThreadID == threadID {
			out = append(out, clone(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Delete removes a deck folder and its metadata.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteLocked(id)
}

// DeleteThread removes every deck owned by a thread and returns their ids.
func (s *Store) DeleteThread(threadID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, d := range s.decks {
		if d.ThreadID == threadID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := s.deleteLocked(id); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// Sweep deletes decks idle for longer than ttl and returns their ids.
func (s *Store) Sweep(ttl time.Duration) ([]string, error) {
	if ttl <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-ttl)
	var ids []string
	for id, d := range s.decks {
		if d.LastAccess.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := s.deleteLocked(id); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

func (s *Store) deleteLocked(id string) error {
	if _, ok := s.decks[id]; !ok {
		return ErrNotFound
	}
	if err := os.RemoveAll(filepath.Join(s.slides, id)); err != nil {
		return err
	}
	delete(s.decks, id)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	list := make([]*Deck, 0, len(s.decks))
	for _, d := range s.decks {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.metaPath, b)
}

// Hash returns the content hash used for optimistic concurrency.
func Hash(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:])[:16]
}

// ParseComments extracts @slide-comment markers with their line numbers.
func ParseComments(src string) []Comment {
	var out []Comment
	for _, m := range commentRe.FindAllStringSubmatchIndex(src, -1) {
		line := 1 + strings.Count(src[:m[0]], "\n")
		out = append(out, Comment{ID: src[m[2]:m[3]], TS: src[m[4]:m[5]], Text: src[m[6]:m[7]], Line: line})
	}
	return out
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func clone(d *Deck) *Deck {
	c := *d
	return &c
}
