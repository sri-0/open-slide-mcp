package shell

import (
	"encoding/json"
	"regexp"
)

// Theme sync for embedders.
//
// open-slide's UI takes its light/dark choice only from localStorage "theme"
// (a pre-paint script in index.html, then next-themes). It has no URL
// parameter and no postMessage API, so the server injects a small script
// right after <head> in the shell page. That script runs before open-slide's
// own pre-paint script.

// themes are the values accepted from ?theme= and from theme messages.
var themes = map[string]bool{"light": true, "dark": true, "system": true}

// themeMessageType is the postMessage type an embedding page sends.
const themeMessageType = "open-slide:theme"

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
// pre-paint script runs, and a {type:"` + themeMessageType + `",theme} message updates
// it live through the storage event next-themes listens for.
(function(){var ok=["light","dark","system"],allow=` + string(origins) + `;
function set(t){try{localStorage.setItem("theme",t)}catch(e){}}
var q=new URLSearchParams(location.search).get("theme");if(ok.indexOf(q)>=0)set(q);
addEventListener("message",function(e){var d=e.data;
if(!d||d.type!=="` + themeMessageType + `"||ok.indexOf(d.theme)<0)return;
if(allow.length&&allow.indexOf(e.origin)<0)return;
set(d.theme);dispatchEvent(new StorageEvent("storage",{key:"theme",newValue:d.theme}));});})();
</script>`)
}
