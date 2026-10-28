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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Deck is the metadata record for one slide folder.
type Deck struct {
	ID         string    `json:"id"`
	ThreadID   string    `json:"thread_id"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastAccess time.Time `json:"last_access"`
	BuildHash  string    `json:"build_hash,omitempty"` // hash of the compiled module served for this deck
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
	ErrBadThread = errors.New("thread_id is required")
)

var commentRe = regexp.MustCompile(`\{/\*\s*@slide-comment\s+id="(c-[a-f0-9]+)"\s+ts="([^"]+)"\s+text="([A-Za-z0-9_\-]+={0,2})"\s*\*/\}`)

// Store owns the slides directory, the compiled builds and the metadata index.
type Store struct {
	root     string     // workspace dir
	slides   string     // <root>/slides
	builds   string     // <root>/.open-slide-mcp/builds/<id>/<hash>.js
	metaPath string     // <root>/.open-slide-mcp/decks.json
	mu       sync.Mutex // guards decks, current and every file write
	decks    map[string]*Deck
	current  map[string]Current
}

// Open loads (or creates) the index for the given workspace.
func Open(root string) (*Store, error) {
	s := &Store{
		root:     root,
		slides:   filepath.Join(root, "slides"),
		builds:   filepath.Join(root, ".open-slide-mcp", "builds"),
		metaPath: filepath.Join(root, ".open-slide-mcp", "decks.json"),
		decks:    map[string]*Deck{},
		current:  map[string]Current{},
	}
	if err := os.MkdirAll(s.slides, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.builds, 0o755); err != nil {
		return nil, err
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

func (s *Store) indexPath(id string) string {
	return filepath.Join(s.slides, id, "index.tsx")
}

// Create writes a new deck folder with the given TSX. The id is a UUIDv7,
// which is also the deck's host label, so it must stay short and plain.
func (s *Store) Create(threadID, title, tsx string) (*Deck, string, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, "", ErrBadThread
	}
	id := uuid.Must(uuid.NewV7()).String()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.slides, id), 0o755); err != nil {
		return nil, "", err
	}
	if err := writeAtomic(s.indexPath(id), []byte(tsx)); err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	d := &Deck{ID: id, ThreadID: threadID, Title: title, CreatedAt: now, UpdatedAt: now, LastAccess: now}
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
	d.LastAccess = time.Now().UTC()
	src := string(b)
	return clone(d), src, Hash(src), ParseComments(src), s.saveLocked()
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
	now := time.Now().UTC()
	d.UpdatedAt, d.LastAccess = now, now
	return clone(d), Hash(tsx), s.saveLocked()
}

// WriteBuild stores the compiled module for a deck and makes it the one the
// deck's origin serves. Older builds are removed so an old hash cannot be
// fetched once a new source has compiled.
func (s *Store) WriteBuild(id string, js, srcmap []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decks[id]
	if !ok {
		return "", ErrNotFound
	}
	hash := Hash(string(js))
	dir := filepath.Join(s.builds, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(dir, hash+".js.map"), srcmap); err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(dir, hash+".js"), js); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), hash+".") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	d.BuildHash = hash
	return hash, s.saveLocked()
}

// BuildPath returns the file for a deck's current module or source map and
// its hash, or false when the deck has never compiled.
func (s *Store) BuildPath(id, ext string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decks[id]
	if !ok || d.BuildHash == "" || (ext != ".js" && ext != ".js.map") {
		return "", "", false
	}
	return filepath.Join(s.builds, id, d.BuildHash+ext), d.BuildHash, true
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
		d.LastAccess = time.Now().UTC()
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
	slices.SortFunc(out, func(a, b *Deck) int { return a.CreatedAt.Compare(b.CreatedAt) })
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
	slices.Sort(ids)
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
	cutoff := time.Now().Add(-ttl)
	var ids []string
	for id, d := range s.decks {
		if d.LastAccess.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
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
	if err := os.RemoveAll(filepath.Join(s.builds, id)); err != nil {
		return err
	}
	delete(s.decks, id)
	delete(s.current, id)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	list := make([]*Deck, 0, len(s.decks))
	for _, d := range s.decks {
		list = append(list, d)
	}
	slices.SortFunc(list, func(a, b *Deck) int { return strings.Compare(a.ID, b.ID) })
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
