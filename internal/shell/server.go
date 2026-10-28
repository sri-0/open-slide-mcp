// Package shell serves the prebuilt open-slide editor and one compiled deck
// on each deck's own origin, behind a signed deck token. The browser's
// same-origin policy is the isolation boundary; this server only has to make
// sure a host answers for its own deck and nothing else, and that nothing is
// served without that deck's session cookie.
package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/sri-0/open-slide-mcp/internal/workspace"
	"github.com/sri-0/open-slide-mcp/pkg/auth"
	"github.com/sri-0/open-slide-mcp/pkg/events"
)

// Server is the handler set for deck origins.
type Server struct {
	Origins Origins
	Dir     string // built shell: index.html, assets/, favicon.*
	Signer  *auth.Signer
	// Cookie names the session cookie. Its __Secure- prefix makes the
	// browser refuse it without Secure; it is set host-only (no Domain) on
	// the deck's base path, so it never reaches a sibling deck host or, in
	// path mode, a sibling deck's paths.
	Cookie       string
	LinkTTL      time.Duration // lifetime of a preview link
	SessionTTL   time.Duration // lifetime of the cookie a link is exchanged for
	Store        *workspace.Store
	Events       *events.Broker
	Editor       http.Handler // the editor's /__* save routes; nil when editing is off
	EmbedOrigins []string     // allowed frame-ancestors and theme-message senders; empty allows any
	Log          *slog.Logger

	index  []byte // index.html with the theme script injected
	head   int    // offset just past <head>, where the per-request base meta goes
	csp    string
	assets http.Handler
}

// scriptRe finds script open tags so each can carry the request's CSP nonce.
var scriptRe = regexp.MustCompile(`(?i)<script(\s|>)`)

// New reads the built shell page and prepares the per-origin handler.
func New(s Server) (*Server, error) {
	page, err := os.ReadFile(filepath.Join(s.Dir, "index.html"))
	if err != nil {
		return nil, fmt.Errorf("shell page: %w", err)
	}
	loc := headRe.FindIndex(page)
	if loc == nil {
		return nil, fmt.Errorf("shell page %s has no <head>", s.Dir)
	}
	s.index = append(append(append([]byte{}, page[:loc[1]]...), themeScript(s.EmbedOrigins)...), page[loc[1]:]...)
	s.head = loc[1]
	ancestors := "*"
	if len(s.EmbedOrigins) > 0 {
		ancestors = strings.Join(s.EmbedOrigins, " ")
	}
	// Inline styles stay allowed because decks append <style> for keyframes
	// and fonts, which only affects their own frame; scripts need a nonce.
	s.csp = "default-src 'none'; script-src 'self' 'nonce-%s'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors " + ancestors + "; base-uri 'none'; form-action 'none'; object-src 'none'"
	s.assets = http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(s.Dir, "assets"))))
	return &s, nil
}

// PreviewURL returns a signed link that opens deckID on its own origin, and
// when the link expires.
func (s *Server) PreviewURL(deckID string) (string, time.Time) {
	tok, exp, err := s.Signer.Mint(deckID, auth.UseLink, s.LinkTTL)
	if err != nil {
		s.Log.Error("mint preview link", "err", err)
		return "", time.Time{}
	}
	return s.Origins.URL(deckID) + "/?t=" + tok, exp
}

// Middleware answers every request addressed to a deck and passes the rest
// on, so the MCP endpoint is unreachable through a deck's URL and deck routes
// are unreachable anywhere else. In path mode the shell's static assets are
// public at the shared origin's root, because the page references them by
// absolute path.
func (s *Server) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			deck, rest, ok := s.Origins.Deck(r.Host, r.URL.Path)
			if !ok {
				if s.Origins.PathMode() && strings.EqualFold(r.Host, s.Origins.Origin("")[len(s.Origins.scheme)+3:]) && strings.HasPrefix(r.URL.Path, "/assets/") {
					c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					s.assets.ServeHTTP(c.Response(), r)
					return nil
				}
				return next(c)
			}
			c.Response().Header().Set("X-Content-Type-Options", "nosniff")
			return s.serve(c, deck, rest)
		}
	}
}

type deckKey struct{}

// DeckFrom returns the deck a request was authenticated for, set for the
// Editor handler.
func DeckFrom(r *http.Request) string {
	d, _ := r.Context().Value(deckKey{}).(string)
	return d
}

func (s *Server) serve(c echo.Context, deck, p string) error {
	r := c.Request()
	if _, err := s.Store.Get(deck); err != nil {
		return c.String(http.StatusNotFound, "unknown deck")
	}
	if p == "/" && c.QueryParam("t") != "" {
		return s.exchange(c, deck)
	}
	ck, err := c.Cookie(s.Cookie)
	if err != nil {
		return c.String(http.StatusUnauthorized, "open the deck through its preview_url first")
	}
	if got, err := s.Signer.Verify(ck.Value, auth.UseSession); err != nil || got != deck {
		return c.String(http.StatusUnauthorized, "deck session expired or for another deck; open the deck through its preview_url again")
	}
	// The cookie is SameSite=None so the iframe can carry it, which means a
	// cross-site page could post with it; only the deck's own page may write.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Header.Get("Origin") != s.Origins.Origin(deck) && r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			return c.String(http.StatusForbidden, "writes are accepted only from the deck's own page")
		}
		if ct := r.Header.Get("Content-Type"); r.ContentLength != 0 && !strings.HasPrefix(ct, "application/json") {
			return c.String(http.StatusUnsupportedMediaType, "writes must be application/json")
		}
	}
	switch {
	case p == "/":
		return c.Redirect(http.StatusFound, s.Origins.Base(deck)+"s/"+url.PathEscape(deck))
	case p == "/manifest.json":
		return s.manifest(c, deck)
	case strings.HasPrefix(p, "/d/"):
		return s.build(c, deck, p)
	case strings.HasPrefix(p, "/assets/"):
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		s.assets.ServeHTTP(c.Response(), r)
		return nil
	case p == "/favicon.ico" || p == "/favicon.svg":
		return c.File(filepath.Join(s.Dir, p[1:]))
	case p == "/__events" && r.Method == http.MethodGet:
		s.Events.Serve(deck, c.Response(), r)
		return nil
	case p == "/__current" && r.Method == http.MethodPost:
		var data map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(c.Response(), r.Body, 64<<10)).Decode(&data); err != nil {
			return c.String(http.StatusBadRequest, "cursor must be a JSON object")
		}
		if err := s.Store.SetCurrent(deck, data); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	case strings.HasPrefix(p, "/__"):
		if s.Editor == nil {
			return c.String(http.StatusNotFound, "editing is not enabled on this server")
		}
		r2 := r.WithContext(context.WithValue(r.Context(), deckKey{}, deck))
		r2.URL.Path = p
		s.Editor.ServeHTTP(c.Response(), r2)
		return nil
	case r.Method == http.MethodGet:
		return s.page(c, deck)
	}
	return c.NoContent(http.StatusMethodNotAllowed)
}

// exchange turns a valid preview link into the session cookie and sends the
// browser to the deck without the token in the address bar.
func (s *Server) exchange(c echo.Context, deck string) error {
	got, err := s.Signer.Verify(c.QueryParam("t"), auth.UseLink)
	if err != nil || got != deck {
		return c.String(http.StatusForbidden, "invalid or expired preview link; ask for a new preview_url")
	}
	tok, exp, err := s.Signer.Mint(deck, auth.UseSession, s.SessionTTL)
	if err != nil {
		return err
	}
	// Partitioned: the deck is iframed by another site, so without CHIPS a
	// browser that blocks third-party cookies would drop it.
	c.SetCookie(&http.Cookie{Name: s.Cookie, Value: tok, Path: s.Origins.Base(deck), HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode, Partitioned: true, Expires: exp})
	s.Store.Touch(deck)
	loc := s.Origins.Base(deck) + "s/" + url.PathEscape(deck)
	if t := c.QueryParam("theme"); themes[t] {
		loc += "?theme=" + t
	}
	return c.Redirect(http.StatusFound, loc)
}

// page serves the shell for any app route with a fresh CSP nonce on each
// inline script.
func (s *Server) page(c echo.Context, deck string) error {
	s.Store.Touch(deck)
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	nonce := base64.StdEncoding.EncodeToString(b[:])
	h := c.Response().Header()
	h.Set("Content-Security-Policy", fmt.Sprintf(s.csp, nonce))
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), display-capture=(), fullscreen=(self)")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	// The shell reads its base from this meta before anything else runs.
	meta := `<meta name="osmcp-base" content="` + s.Origins.Base(deck) + `">`
	body := append(append(append([]byte{}, s.index[:s.head]...), meta...), s.index[s.head:]...)
	body = scriptRe.ReplaceAll(body, []byte(`<script nonce="`+nonce+`"$1`))
	return c.Blob(http.StatusOK, "text/html; charset=utf-8", body)
}

// manifest lists the one deck this origin serves, in the shape the shell's
// runtime slides module reads.
func (s *Server) manifest(c echo.Context, deck string) error {
	d, err := s.Store.Get(deck)
	if err != nil {
		return err
	}
	type entry struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Theme     string `json:"theme"`
		CreatedAt string `json:"createdAt"`
		URL       string `json:"url"`
	}
	slides := []entry{}
	if d.BuildHash != "" {
		slides = append(slides, entry{ID: d.ID, Title: d.Title, CreatedAt: d.CreatedAt.Format(time.RFC3339), URL: s.Origins.Base(deck) + "d/index.js"})
	}
	c.Response().Header().Set("Cache-Control", "no-cache")
	return c.JSON(http.StatusOK, map[string]any{"slides": slides})
}

// build serves the deck's current module or source map at a stable URL,
// because the editor re-imports the URL it first loaded whenever the deck
// changes; the hash travels as the ETag so unchanged builds revalidate.
func (s *Server) build(c echo.Context, deck, p string) error {
	path, hash, ok := s.Store.BuildPath(deck, strings.TrimPrefix(p, "/d/index"))
	if !ok {
		return c.NoContent(http.StatusNotFound)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ct := "text/javascript; charset=utf-8"
	if strings.HasSuffix(path, ".map") {
		ct = "application/json"
	}
	h := c.Response().Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", `"`+hash+`"`)
	if c.Request().Header.Get("If-None-Match") == `"`+hash+`"` {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Stream(http.StatusOK, ct, bytes.NewReader(body))
}
