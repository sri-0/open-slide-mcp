// Package build compiles a deck's source and, when it compiles, makes the
// result the module the deck's origin serves and tells open editors to
// reload it. Both the MCP tools and the editor's save routes go through it,
// so an agent's write and a person's edit reach the preview the same way.
package build

import (
	"context"

	"github.com/sri-0/open-slide-mcp/internal/workspace"
	"github.com/sri-0/open-slide-mcp/pkg/compile"
	"github.com/sri-0/open-slide-mcp/pkg/events"
)

// Deps are the collaborators a build needs.
type Deps struct {
	Store    *workspace.Store
	Compiler *compile.Compiler
	Events   *events.Broker
}

// Deck compiles src for deckID. A failure is returned in the Result, never
// as an error, so callers report it and the previous build keeps serving.
func (d Deps) Deck(ctx context.Context, deckID, src string) compile.Result {
	r, err := d.Compiler.Compile(ctx, []byte(src))
	if err != nil {
		return compile.Result{OK: false, Message: err.Error()}
	}
	if !r.OK {
		return r
	}
	if _, err := d.Store.WriteBuild(deckID, r.JS, r.Map); err != nil {
		return compile.Result{OK: false, Message: "store build: " + err.Error()}
	}
	d.Events.Publish(deckID, "open-slide:slide-changed", map[string]any{"slideIds": []string{deckID}})
	r.JS, r.Map = nil, nil
	return r
}
