package proxy

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

const page = `<!doctype html><html><head><title>x</title><script>/* core theme */</script></head><body></body></html>`

// upstream mimics the dev server: HTML for documents (gzipped when the client
// allows it), JS for modules, and records the Accept-Encoding it saw.
func upstream(t *testing.T, sawEncoding *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Content-Type", "text/javascript")
			io.WriteString(w, `const s = "<head>";`)
		case r.URL.Path == "/healthz.json":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"html":"<head>"}`)
		default:
			*sawEncoding = r.Header.Get("Accept-Encoding")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Etag", `W/"abc"`)
			if strings.Contains(*sawEncoding, "gzip") {
				w.Header().Set("Content-Encoding", "gzip")
				gz := gzip.NewWriter(w)
				io.WriteString(gz, page)
				gz.Close()
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(page)))
			io.WriteString(w, page)
		}
	}))
}

func newTestProxy(t *testing.T, target string, origins []string) *echo.Echo {
	p, err := New(target, []byte("s"), false, time.Hour, "http://localhost:8090", nil)
	if err != nil {
		t.Fatal(err)
	}
	p.EmbedOrigins = origins
	e := echo.New()
	p.Register(e)
	return e
}

func TestInjectThemeIntoHTML(t *testing.T) {
	var saw string
	up := upstream(t, &saw)
	defer up.Close()
	e := newTestProxy(t, up.URL, []string{"http://localhost:3000"})

	req := httptest.NewRequest(http.MethodGet, "/s/deck?theme=dark", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("response still encoded: %q", rec.Header().Get("Content-Encoding"))
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length %s, body %d", got, len(body))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	if rec.Header().Get("Etag") != "" {
		t.Fatal("stale Etag kept on modified page")
	}
	inj := strings.Index(body, ThemeMessageType)
	head := strings.Index(body, "<head>")
	pre := strings.Index(body, "/* core theme */")
	if inj < 0 || !(head < inj && inj < pre) {
		t.Fatalf("script not injected between <head> and the pre-paint script:\n%s", body)
	}
	if !strings.Contains(body, `allow=["http://localhost:3000"]`) {
		t.Fatalf("embed origins missing:\n%s", body)
	}
	if strings.Count(body, "<script>") != 2 {
		t.Fatalf("expected exactly one injected script:\n%s", body)
	}
}

func TestInjectThemeLeavesOtherResponses(t *testing.T) {
	var saw string
	up := upstream(t, &saw)
	defer up.Close()
	e := newTestProxy(t, up.URL, nil)

	for _, path := range []string{"/src/main.js", "/healthz.json"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "*/*")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), ThemeMessageType) {
			t.Fatalf("%s was modified: %s", path, rec.Body.String())
		}
	}
}

func TestThemeScriptOrigins(t *testing.T) {
	if s := string(themeScript(nil)); !strings.Contains(s, "allow=[];") {
		t.Fatalf("nil origins should allow any: %s", s)
	}
	// Origins are JSON-encoded, so they cannot close the script tag.
	if s := string(themeScript([]string{"</script><b>"})); strings.Contains(s, "</script><b>") {
		t.Fatalf("origin not escaped: %s", s)
	}
}

func TestPreviewKeepsTheme(t *testing.T) {
	e := newTestProxy(t, "http://127.0.0.1:1", nil)
	cases := map[string]string{
		"/preview/deck":                "/s/deck",
		"/preview/deck?theme=dark":     "/s/deck?theme=dark",
		"/preview/deck?theme=light":    "/s/deck?theme=light",
		"/preview/deck?theme=system":   "/s/deck?theme=system",
		"/preview/deck?theme=blue":     "/s/deck",
		"/preview/deck?x=1&theme=dark": "/s/deck?theme=dark",
	}
	for in, want := range cases {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, in, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %q, want %q", in, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}

func TestPreviewKeepsThemeWithAuth(t *testing.T) {
	p, _ := New("http://127.0.0.1:1", []byte("s"), true, time.Hour, "http://localhost:8090", nil)
	e := echo.New()
	p.Register(e)
	u, _ := p.PreviewURL("deck")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(u, "http://localhost:8090")+"&theme=dark", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/s/deck?theme=dark" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Set-Cookie") == "" {
		t.Fatal("session cookie not set")
	}
}
