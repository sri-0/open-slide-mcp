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
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sri-0/open-slide-mcp/internal/build"
	"github.com/sri-0/open-slide-mcp/internal/shell"
)

// Editor is the http.Handler the shell mounts under /__.
type Editor struct {
	Build build.Builder
	Apply Applier // the engine that patches source: the Node sidecar or the embedded interpreter
	Log   *slog.Logger
}

// Applier applies one editor request to a deck's source. Both engines run
// open-slide's own editing functions on the same bundle; they differ only
// in where that JavaScript runs.
type Applier interface {
	Apply(ctx context.Context, req Request) (Response, error)
}

// Request is one editor save call with the source it applies to.
type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
	Source string            `json:"source"`
}

// Response is what the browser receives, plus the new source when it changed.
type Response struct {
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
	case strings.HasPrefix(p, "/__slides/") && !strings.HasPrefix(strings.TrimPrefix(p, "/__slides/"), deck+"/"):
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

// apply hands one request and the deck's source to the engine and keeps
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
	out, err := e.Apply.Apply(r.Context(), Request{Method: r.Method, Path: r.URL.Path, Query: q, Body: body, Source: src})
	if err != nil {
		return err
	}
	if out.Source != "" && out.Source != src {
		if _, _, err := e.Build.Store.Write(deck, out.Source, ""); err != nil {
			return err
		}
		if res := e.Build.Build(r.Context(), deck, out.Source); !res.OK {
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
