package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Theme sync for embedders.
//
// open-slide's UI takes its light/dark choice only from localStorage "theme"
// (a pre-paint script in index.html, then next-themes). It has no URL
// parameter and no postMessage API, so the proxy injects a small script right
// after <head> in every HTML page it serves. That script runs before
// open-slide's own pre-paint script.

// themes are the values accepted from ?theme= and from theme messages.
var themes = map[string]bool{"light": true, "dark": true, "system": true}

// ThemeMessageType is the postMessage type an embedding page sends.
const ThemeMessageType = "open-slide:theme"

// headRe matches the opening <head> tag (not <header>).
var headRe = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)

// themeScript returns the inline script. allowed lists the embedder origins
// whose messages are accepted; empty accepts any origin. Only the theme value
// is ever used, and it is checked against the three known values.
func themeScript(allowed []string) []byte {
	if allowed == nil {
		allowed = []string{}
	}
	origins, _ := json.Marshal(allowed) // json escapes <, > and & so this is safe inside <script>
	return []byte(`<script>
// Injected by open-slide-mcp so an embedding page can set the editor theme.
// open-slide reads only localStorage "theme": ?theme= seeds it before the
// pre-paint script runs, and a {type:"` + ThemeMessageType + `",theme} message updates
// it live through the storage event next-themes listens for.
(function(){var ok=["light","dark","system"],allow=` + string(origins) + `;
function set(t){try{localStorage.setItem("theme",t)}catch(e){}}
var q=new URLSearchParams(location.search).get("theme");if(ok.indexOf(q)>=0)set(q);
addEventListener("message",function(e){var d=e.data;
if(!d||d.type!=="` + ThemeMessageType + `"||ok.indexOf(d.theme)<0)return;
if(allow.length&&allow.indexOf(e.origin)<0)return;
set(d.theme);dispatchEvent(new StorageEvent("storage",{key:"theme",newValue:d.theme}));});})();
</script>`)
}

// wantsHTML reports whether a request may be answered with an HTML page.
func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// injectTheme is the reverse proxy's ModifyResponse. It rewrites successful,
// uncompressed text/html responses and leaves everything else (scripts,
// styles, assets, websocket upgrades) untouched.
func (p *Proxy) injectTheme(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.Method != http.MethodGet {
		return nil
	}
	mt, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt != "text/html" {
		return nil
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		return nil // the rewrite asks for identity, so this only happens if upstream ignores it
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if loc := headRe.FindIndex(body); loc != nil {
		script := themeScript(p.EmbedOrigins)
		out := make([]byte, 0, len(body)+len(script))
		out = append(out, body[:loc[1]]...)
		out = append(out, script...)
		out = append(out, body[loc[1]:]...)
		body = out
		// The upstream validator describes the unmodified page.
		resp.Header.Del("Etag")
		// The script pushes open-slide's <meta charset> past the first 1024
		// bytes browsers prescan, so declare it in the header. The dev server
		// always serves UTF-8.
		if params["charset"] == "" {
			resp.Header.Set("Content-Type", "text/html; charset=utf-8")
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}
