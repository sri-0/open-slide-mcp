// Package editor serves the open-slide editor's save routes for the deck a
// request was authenticated for: it reads that deck's source, has the
// parse-only sidecar apply the browser's request to the text, writes the
// result back, recompiles it and tells open editors to reload. The shell
// server has already checked the host and the cookie; this package never
// looks at either.
package editor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sri-0/open-slide-mcp/internal/build"
	"github.com/sri-0/open-slide-mcp/internal/shell"
)

// Editor is the http.Handler the shell mounts under /__.
type Editor struct {
	Build   build.Deps
	Sidecar string // base URL of the sidecar, e.g. http://127.0.0.1:8091
	Log     *slog.Logger
	client  *http.Client
}

// New returns an Editor with a bounded client for the sidecar.
func New(e Editor) *Editor {
	e.client = &http.Client{Timeout: 15 * time.Second}
	return &e
}

type applyRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
	Source string            `json:"source"`
}

type applyResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
	Source string          `json:"source,omitempty"`
}

// ServeHTTP answers the routes the editor UI calls. Routes that touched the
// dev server's process, package manager or the network do not exist here.
func (e *Editor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	deck := shell.DeckFrom(r)
	p := r.URL.Path
	switch {
	case deck == "":
		http.Error(w, "no deck", http.StatusInternalServerError)
	case p == "/__server-status":
		writeJSON(w, http.StatusOK, map[string]any{"executionId": "static", "canRestart": false})
	case p == "/__folders" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"folders": []any{}, "assignments": map[string]any{}})
	case strings.HasPrefix(p, "/__assets/") && r.Method == http.MethodGet && strings.Count(p, "/") == 2:
		writeJSON(w, http.StatusOK, map[string]any{"assets": []any{}})
	case strings.HasPrefix(p, "/__slides/") && strings.SplitN(strings.TrimPrefix(p, "/__slides/"), "/", 2)[0] != deck:
		http.Error(w, "this session only edits its own deck", http.StatusForbidden)
	case strings.HasPrefix(p, "/__edit"), strings.HasPrefix(p, "/__comments"), strings.HasPrefix(p, "/__design"),
		strings.HasPrefix(p, "/__notes"), strings.HasPrefix(p, "/__slides/"):
		if err := e.apply(w, r, deck); err != nil {
			e.Log.Error("editor route", "path", p, "deck", deck, "err", err)
			http.Error(w, "edit failed: "+err.Error(), http.StatusBadGateway)
		}
	default:
		http.Error(w, "not available on this server", http.StatusNotFound)
	}
}

// apply forwards one request and the deck's source to the sidecar and keeps
// whatever source comes back.
func (e *Editor) apply(w http.ResponseWriter, r *http.Request, deck string) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("null")
	}
	if !json.Valid(body) {
		http.Error(w, "body must be JSON", http.StatusBadRequest)
		return nil
	}
	_, src, _, _, err := e.Build.Store.Read(deck)
	if err != nil {
		return err
	}
	q := map[string]string{}
	for k, v := range r.URL.Query() {
		q[k] = v[0]
	}
	// The browser addresses the deck by the id it sees; the sidecar only
	// ever receives the source of the deck the session is for.
	q["slideId"] = deck
	reqBody, err := json.Marshal(applyRequest{Method: r.Method, Path: r.URL.Path, Query: q, Body: body, Source: src})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, e.Sidecar+"/apply", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("sidecar: %w", err)
	}
	defer resp.Body.Close()
	var out applyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sidecar returned %d: %w", resp.StatusCode, errors.Join(err, errors.New("unreadable response")))
	}
	if out.Source != "" && out.Source != src {
		if _, _, err := e.Build.Store.Write(deck, out.Source, ""); err != nil {
			return err
		}
		if res := e.Build.Deck(r.Context(), deck, out.Source); !res.OK {
			e.Log.Warn("edited deck does not compile", "deck", deck, "msg", res.Message, "line", res.Line)
		}
	}
	if out.Body == nil {
		out.Body = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(out.Status)
	_, err = w.Write(out.Body)
	return err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WaitReady polls the sidecar until it answers or the deadline passes.
func WaitReady(ctx context.Context, base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("sidecar did not become ready at " + base)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
