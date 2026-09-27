package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeckID(t *testing.T) {
	id, err := DeckID("thread_ABC 123", "q2-roadmap")
	if err != nil {
		t.Fatal(err)
	}
	if want := "thread-abc-123-"; id[:len(want)] != want || id[len(id)-len("-q2-roadmap"):] != "-q2-roadmap" {
		t.Fatalf("unexpected id %q", id)
	}
	if _, err := DeckID("", "x"); !errors.Is(err, ErrBadThread) {
		t.Fatalf("want ErrBadThread, got %v", err)
	}
	if _, err := DeckID("t", "Bad Name"); !errors.Is(err, ErrBadName) {
		t.Fatalf("want ErrBadName, got %v", err)
	}
}

func TestLifecycle(t *testing.T) {
	s := newStore(t)
	d, h, err := s.Create("thread-1", "intro", "Intro", "export default [];")
	if err != nil {
		t.Fatal(err)
	}
	if h != Hash("export default [];") {
		t.Fatal("hash mismatch")
	}
	if _, _, err := s.Create("thread-1", "intro", "Intro", ""); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if _, _, err := s.Write(d.ID, "changed", "deadbeef"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if _, h2, err := s.Write(d.ID, "changed", h); err != nil || h2 != Hash("changed") {
		t.Fatalf("write: %v", err)
	}
	_, src, _, _, err := s.Read(d.ID)
	if err != nil || src != "changed" {
		t.Fatalf("read: %v %q", err, src)
	}
	if got := len(s.List("thread-1")); got != 1 {
		t.Fatalf("list: %d", got)
	}
	if got := len(s.List("other")); got != 0 {
		t.Fatalf("list other: %d", got)
	}

	// Reopen and confirm the index survives.
	s2, err := Open(s.Root())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Get(d.ID); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	ids, err := s2.DeleteThread("thread-1")
	if err != nil || len(ids) != 1 {
		t.Fatalf("delete thread: %v %v", ids, err)
	}
	if _, err := os.Stat(filepath.Dir(s2.IndexPath(d.ID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("folder still present")
	}
}

func TestSweep(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	old, _, _ := s.Create("t", "old", "Old", "")
	s.now = func() time.Time { return base.Add(2 * time.Hour) }
	fresh, _, _ := s.Create("t", "fresh", "Fresh", "")
	s.now = func() time.Time { return base.Add(3 * time.Hour) }
	ids, err := s.Sweep(90 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != old.ID {
		t.Fatalf("swept %v", ids)
	}
	if _, err := s.Get(fresh.ID); err != nil {
		t.Fatal("fresh deck removed")
	}
}

func TestParseComments(t *testing.T) {
	src := "const A = () => (\n  <div>\n    {/* @slide-comment id=\"c-1a2b3c4d\" ts=\"2026-05-01T00:00:00Z\" text=\"eyJ0IjoxfQ\" */}\n  </div>\n);"
	cs := ParseComments(src)
	if len(cs) != 1 || cs[0].ID != "c-1a2b3c4d" || cs[0].Line != 3 {
		t.Fatalf("got %+v", cs)
	}
}
