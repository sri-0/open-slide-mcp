// Command smoke drives the MCP endpoint through a full deck lifecycle.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	url := flag.String("url", "http://localhost:8090/mcp", "MCP endpoint")
	keep := flag.Bool("keep", false, "leave the deck in place instead of cleaning up")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: *url, HTTPClient: http.DefaultClient}, nil)
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
	resp, err := http.Get(pv["preview_url"].(string))
	must(err)
	resp.Body.Close()
	fmt.Println("preview GET status:", resp.StatusCode)

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
