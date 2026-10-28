// Command smoke drives the MCP endpoint through a full deck lifecycle.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	endpoint := flag.String("url", "http://localhost:8090/mcp", "MCP endpoint")
	keep := flag.Bool("keep", false, "leave the deck in place instead of cleaning up")
	flag.Parse()
	// Deck origins are *.decks.localhost, which browsers resolve to loopback
	// but Go's resolver may not, so dial every host at the MCP endpoint's address.
	addr, _ := url.Parse(*endpoint)
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr.Host)
	}
	// The session cookie is Secure, which browsers accept on *.localhost over
	// http but Go's cookie jar does not, so the cookie is carried by hand.
	browser := &http.Client{Transport: &http.Transport{DialContext: dial}}
	var session *http.Cookie
	browser.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if session != nil {
			req.AddCookie(session)
		}
		return nil
	}
	get := func(u string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		must(err)
		if session != nil {
			req.AddCookie(session)
		}
		resp, err := browser.Do(req)
		must(err)
		return resp
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: *endpoint, HTTPClient: http.DefaultClient}, nil)
	must(err)
	defer sess.Close()

	tl, err := sess.ListTools(ctx, nil)
	must(err)
	var names []string
	for _, t := range tl.Tools {
		names = append(names, t.Name)
	}
	fmt.Println("tools:", strings.Join(names, " "))

	thread := "smoke-" + time.Now().Format("150405")
	// thread_id enforcement
	res := call(ctx, sess, "deck_create", map[string]any{"title": "No thread"})
	if !res.IsError {
		fail("deck_create without thread_id should fail")
	}
	fmt.Println("thread_id enforced:", text(res))

	out := structured(call(ctx, sess, "deck_create", map[string]any{"thread_id": thread, "title": "Smoke Test Deck"}))
	deck := out["deck_id"].(string)
	hash := out["hash"].(string)
	fmt.Println("created:", deck, "preview:", out["preview_url"], "valid:", out["validation"].(map[string]any)["ok"])
	if out["validation"].(map[string]any)["ok"] != true {
		fail("starter deck did not validate: %v", out["validation"])
	}

	// conflict detection
	res = call(ctx, sess, "deck_write", map[string]any{"deck_id": deck, "tsx": "export default [];", "expected_hash": "nope"})
	if !res.IsError {
		fail("stale hash should be rejected")
	}
	fmt.Println("conflict detected:", text(res))

	// broken write reports a compile error
	out = structured(call(ctx, sess, "deck_write", map[string]any{"deck_id": deck, "expected_hash": hash,
		"tsx": "import type { Page } from '@open-slide/core';\nconst A = () => <div>{oops</div>;\nexport default [A] satisfies Page[];\n"}))
	v := out["validation"].(map[string]any)
	if v["ok"] == true {
		fail("broken tsx validated")
	}
	fmt.Printf("compile error surfaced: line %v col %v\n", v["line"], v["column"])
	hash = out["hash"].(string)

	// good write
	good := "import type { Page, SlideMeta } from '@open-slide/core';\nexport const meta: SlideMeta = { title: 'Smoke', createdAt: '2026-09-27T00:00:00Z' };\nconst A: Page = () => <div style={{ width: 1920, height: 1080, display: 'grid', placeItems: 'center', fontSize: 120 }}>Hello from MCP {/* @slide-comment id=\"c-deadbeef\" ts=\"2026-09-27T00:00:00Z\" text=\"eyJ0ZXh0IjoiaGkifQ\" */}</div>;\nexport default [A] satisfies Page[];\n"
	out = structured(call(ctx, sess, "deck_write", map[string]any{"deck_id": deck, "expected_hash": hash, "tsx": good}))
	if out["validation"].(map[string]any)["ok"] != true {
		fail("good tsx failed: %v", out["validation"])
	}
	fmt.Println("good write validated")

	out = structured(call(ctx, sess, "deck_read", map[string]any{"deck_id": deck}))
	fmt.Println("comments found:", len(out["comments"].([]any)))
	out = structured(call(ctx, sess, "deck_list", map[string]any{"thread_id": thread}))
	fmt.Println("decks in thread:", len(out["decks"].([]any)))
	out = structured(call(ctx, sess, "skill_read", map[string]any{"name": "slide-authoring"}))
	fmt.Println("skill bytes:", len(out["content"].(string)))

	pv := structured(call(ctx, sess, "deck_preview_url", map[string]any{"deck_id": deck}))
	link := pv["preview_url"].(string)
	// The deck's base URL: its own origin, or a path on the shared one.
	base := strings.TrimSuffix(link[:strings.Index(link, "?t=")], "/")
	origin, _ := url.Parse(link)
	// A bare base has no session yet.
	resp := get(base + "/manifest.json")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		fail("manifest without a session returned %d, want 401", resp.StatusCode)
	}
	// The link exchanges for a cookie and lands on the editor.
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp = get(link)
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "__Secure-") {
			session = c
		}
	}
	if resp.StatusCode != http.StatusFound || session == nil {
		fail("preview link returned %d without a session cookie", resp.StatusCode)
	}
	resp = get(origin.Scheme + "://" + origin.Host + resp.Header.Get("Location"))
	resp.Body.Close()
	fmt.Println("preview GET status:", resp.StatusCode, "at:", resp.Request.URL.Path)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Request.URL.Path, "/s/") {
		fail("preview link did not open the editor")
	}
	resp = get(base + "/manifest.json")
	var manifest struct {
		Slides []struct{ ID, URL string }
	}
	must(json.NewDecoder(resp.Body).Decode(&manifest))
	resp.Body.Close()
	if len(manifest.Slides) != 1 || manifest.Slides[0].ID != deck {
		fail("manifest lists %v, want only %s", manifest.Slides, deck)
	}
	resp = get(origin.Scheme + "://" + origin.Host + manifest.Slides[0].URL)
	resp.Body.Close()
	fmt.Println("compiled module status:", resp.StatusCode, "cache:", resp.Header.Get("Cache-Control"))
	// The same session must be refused on another deck's URL.
	resp = get(strings.Replace(base, deck, "00000000-0000-7000-8000-000000000000", 1) + "/manifest.json")
	resp.Body.Close()
	fmt.Println("other origin status:", resp.StatusCode)
	if resp.StatusCode == http.StatusOK {
		fail("deck session was accepted on another deck's origin")
	}

	if *keep {
		fmt.Println("kept deck", deck, "at", pv["preview_url"])
		return
	}
	out = structured(call(ctx, sess, "thread_cleanup", map[string]any{"thread_id": thread}))
	fmt.Println("cleaned:", out["deleted"])
	fmt.Println("SMOKE OK")
}

func call(ctx context.Context, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	must(err)
	return res
}

func structured(res *mcp.CallToolResult) map[string]any {
	if res.IsError {
		fail("tool error: %s", text(res))
	}
	b, _ := json.Marshal(res.StructuredContent)
	var m map[string]any
	must(json.Unmarshal(b, &m))
	return m
}

func text(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return t.Text
		}
	}
	return ""
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+f+"\n", a...)
	os.Exit(1)
}
