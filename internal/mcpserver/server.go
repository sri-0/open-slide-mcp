// Package mcpserver exposes the deck workspace as MCP tools, resources and
// prompts using the official Go SDK.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/prismgroup/open-slide-mcp/internal/skills"
	"github.com/prismgroup/open-slide-mcp/internal/validate"
	"github.com/prismgroup/open-slide-mcp/internal/workspace"
)

// Deps are the collaborators the tools need.
type Deps struct {
	Store      *workspace.Store
	ViteBase   string
	PreviewURL func(deckID string) (string, time.Time)
	Log        *slog.Logger
	Version    string
}

// New builds the MCP server with every tool registered.
func New(d Deps) (*mcp.Server, error) {
	s := mcp.NewServer(&mcp.Implementation{Name: "open-slide-mcp", Version: d.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	t := &tools{d}
	mcp.AddTool(s, &mcp.Tool{Name: "deck_create", Description: descCreate}, t.create)
	mcp.AddTool(s, &mcp.Tool{Name: "deck_write", Description: descWrite}, t.write)
	mcp.AddTool(s, &mcp.Tool{Name: "deck_read", Description: descRead}, t.read)
	mcp.AddTool(s, &mcp.Tool{Name: "deck_list", Description: descList}, t.list)
	mcp.AddTool(s, &mcp.Tool{Name: "deck_validate", Description: descValidate}, t.validate)
	mcp.AddTool(s, &mcp.Tool{Name: "deck_preview_url", Description: descPreview}, t.preview)
	mcp.AddTool(s, &mcp.Tool{Name: "deck_current", Description: descCurrent}, t.current)
	mcp.AddTool(s, &mcp.Tool{Name: "deck_delete", Description: descDelete}, t.del)
	mcp.AddTool(s, &mcp.Tool{Name: "thread_cleanup", Description: descCleanup}, t.cleanup)
	mcp.AddTool(s, &mcp.Tool{Name: "skill_read", Description: descSkill}, t.skill)
	if err := registerSkills(s); err != nil {
		return nil, err
	}
	return s, nil
}

const instructions = `open-slide-mcp manages slide decks built with the open-slide framework (React pages on a 1920x1080 canvas).
Workflow: skill_read("slide-authoring") once per session, then deck_create (thread_id is mandatory), deck_write with full TSX, fix any validation errors, and hand the user the preview_url. Users can edit slides in the preview UI; always deck_read before rewriting and pass expected_hash so their edits are not lost. Call thread_cleanup when a conversation is finished with its decks.`

const (
	descCreate   = "Create a new deck owned by a thread. thread_id (or an artifact id) is REQUIRED and scopes every later call; refuse to guess it. name is a short kebab-case id; title is human readable. Pass tsx to seed the deck, otherwise a starter cover page is written. Returns deck_id, content hash, created_at (use it for meta.createdAt) and preview_url."
	descWrite    = "Replace a deck's slides/<id>/index.tsx with the full TSX module. Pass expected_hash from the last deck_read or deck_write; the call fails with a conflict if the user edited the deck in the browser since then. The module is compiled by the dev server; validation errors come back with line and column and the write is kept so you can fix incrementally."
	descRead     = "Read a deck: current TSX, its hash, metadata, and any @slide-comment markers the user left in the inspector (apply them with the apply-comments skill)."
	descList     = "List decks owned by a thread_id. Omit thread_id only when explicitly asked to list every deck on the server."
	descValidate = "Compile a deck through the dev server without changing it. Detects syntax errors and bad imports, not type errors."
	descPreview  = "Return a fresh preview_url for a deck. Embed or open it in a browser; it renders the live deck with the open-slide editor and export controls."
	descCurrent  = "Return the live cursor written by the dev server: which deck, page and inspector-selected element the user is currently looking at. Re-read it every time the user says 'this slide' or 'this element'."
	descDelete   = "Delete one deck and its folder permanently."
	descCleanup  = "Delete every deck owned by a thread_id. Call when the conversation is done with its decks."
	descSkill    = "Read an open-slide authoring skill adapted for MCP use. Names: slide-authoring (technical reference, read first), create-slide (workflow), apply-comments, create-theme, current-slide. Set with_references=true to include the reference docs (transitions, steps, morph, webfonts, assets, design-system, page-numbers)."
)

type tools struct{ Deps }

type deckOut struct {
	DeckID     string          `json:"deck_id"`
	ThreadID   string          `json:"thread_id"`
	Title      string          `json:"title"`
	Hash       string          `json:"hash"`
	CreatedAt  string          `json:"created_at"`
	PreviewURL string          `json:"preview_url,omitempty"`
	Validation validate.Result `json:"validation"`
}

type createIn struct {
	ThreadID string `json:"thread_id" jsonschema:"REQUIRED. The chat thread or artifact id that owns this deck"`
	Name     string `json:"name,omitempty" jsonschema:"kebab-case deck name, e.g. q2-roadmap. Derived from title when omitted"`
	Title    string `json:"title" jsonschema:"human readable deck title"`
	TSX      string `json:"tsx,omitempty" jsonschema:"optional full index.tsx module to seed the deck"`
}

func (t *tools) create(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, deckOut, error) {
	if strings.TrimSpace(in.ThreadID) == "" {
		return nil, deckOut{}, workspace.ErrBadThread
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, deckOut{}, errors.New("title is required")
	}
	name := in.Name
	if name == "" {
		name = workspace.Slugify(in.Title)
	}
	now := time.Now().UTC()
	tsx := in.TSX
	if tsx == "" {
		tsx = workspace.StarterTSX(in.Title, now)
	}
	deck, hash, err := t.Store.Create(in.ThreadID, name, in.Title, tsx)
	if err != nil {
		return nil, deckOut{}, err
	}
	url, _ := t.PreviewURL(deck.ID)
	v, verr := t.check(ctx, deck.ID)
	t.Log.Info("deck created", "deck", deck.ID, "thread", deck.ThreadID, "valid", v.OK)
	return nil, deckOut{DeckID: deck.ID, ThreadID: deck.ThreadID, Title: deck.Title, Hash: hash, CreatedAt: deck.CreatedAt.Format(time.RFC3339), PreviewURL: url, Validation: v}, verr
}

type writeIn struct {
	DeckID       string `json:"deck_id"`
	TSX          string `json:"tsx" jsonschema:"the complete index.tsx module"`
	ExpectedHash string `json:"expected_hash,omitempty" jsonschema:"hash from the last deck_read or deck_write; protects browser edits"`
}

func (t *tools) write(ctx context.Context, _ *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, deckOut, error) {
	if strings.TrimSpace(in.TSX) == "" {
		return nil, deckOut{}, errors.New("tsx is required")
	}
	deck, hash, err := t.Store.Write(in.DeckID, in.TSX, in.ExpectedHash)
	if err != nil {
		return nil, deckOut{}, err
	}
	v, verr := t.check(ctx, deck.ID)
	url, _ := t.PreviewURL(deck.ID)
	t.Log.Info("deck written", "deck", deck.ID, "valid", v.OK)
	return nil, deckOut{DeckID: deck.ID, ThreadID: deck.ThreadID, Title: deck.Title, Hash: hash, CreatedAt: deck.CreatedAt.Format(time.RFC3339), PreviewURL: url, Validation: v}, verr
}

type deckIn struct {
	DeckID string `json:"deck_id"`
}

type readOut struct {
	Deck     *workspace.Deck     `json:"deck"`
	Hash     string              `json:"hash"`
	TSX      string              `json:"tsx"`
	Comments []workspace.Comment `json:"comments"`
}

func (t *tools) read(_ context.Context, _ *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, readOut, error) {
	deck, src, hash, comments, err := t.Store.Read(in.DeckID)
	if err != nil {
		return nil, readOut{}, err
	}
	if comments == nil {
		comments = []workspace.Comment{}
	}
	return nil, readOut{Deck: deck, Hash: hash, TSX: src, Comments: comments}, nil
}

type listIn struct {
	ThreadID string `json:"thread_id,omitempty"`
}

type listOut struct {
	Decks []*workspace.Deck `json:"decks"`
}

func (t *tools) list(_ context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	decks := t.Store.List(in.ThreadID)
	if decks == nil {
		decks = []*workspace.Deck{}
	}
	return nil, listOut{Decks: decks}, nil
}

func (t *tools) validate(ctx context.Context, _ *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, validate.Result, error) {
	if _, err := t.Store.Get(in.DeckID); err != nil {
		return nil, validate.Result{}, err
	}
	r, err := t.check(ctx, in.DeckID)
	return nil, r, err
}

type previewOut struct {
	PreviewURL string `json:"preview_url"`
	ExpiresAt  string `json:"expires_at"`
}

func (t *tools) preview(_ context.Context, _ *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, previewOut, error) {
	if _, err := t.Store.Get(in.DeckID); err != nil {
		return nil, previewOut{}, err
	}
	url, exp := t.PreviewURL(in.DeckID)
	return nil, previewOut{PreviewURL: url, ExpiresAt: exp.UTC().Format(time.RFC3339)}, nil
}

type emptyIn struct{}

type currentOut struct {
	Current workspace.Current `json:"current"`
	Note    string            `json:"note,omitempty"`
}

func (t *tools) current(_ context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, currentOut, error) {
	c, err := t.Store.ReadCurrent()
	if err != nil {
		return nil, currentOut{}, err
	}
	if c == nil {
		return nil, currentOut{Note: "no cursor yet; the user has not opened a deck in the preview"}, nil
	}
	return nil, currentOut{Current: c}, nil
}

type deleteOut struct {
	Deleted []string `json:"deleted"`
}

func (t *tools) del(_ context.Context, _ *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, deleteOut, error) {
	if err := t.Store.Delete(in.DeckID); err != nil {
		return nil, deleteOut{}, err
	}
	t.Log.Info("deck deleted", "deck", in.DeckID)
	return nil, deleteOut{Deleted: []string{in.DeckID}}, nil
}

type cleanupIn struct {
	ThreadID string `json:"thread_id"`
}

func (t *tools) cleanup(_ context.Context, _ *mcp.CallToolRequest, in cleanupIn) (*mcp.CallToolResult, deleteOut, error) {
	if strings.TrimSpace(in.ThreadID) == "" {
		return nil, deleteOut{}, workspace.ErrBadThread
	}
	ids, err := t.Store.DeleteThread(in.ThreadID)
	if ids == nil {
		ids = []string{}
	}
	t.Log.Info("thread cleaned", "thread", in.ThreadID, "deleted", len(ids))
	return nil, deleteOut{Deleted: ids}, err
}

type skillIn struct {
	Name           string `json:"name"`
	WithReferences bool   `json:"with_references,omitempty"`
}

type skillOut struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (t *tools) skill(_ context.Context, _ *mcp.CallToolRequest, in skillIn) (*mcp.CallToolResult, skillOut, error) {
	sk, err := skills.Get(in.Name)
	if err != nil {
		return nil, skillOut{}, err
	}
	return nil, skillOut{Name: sk.Name, Content: sk.Render(in.WithReferences)}, nil
}

// check compiles the deck; a validation failure is reported in the result,
// not as a tool error, so the model sees the message and the write persists.
func (t *tools) check(ctx context.Context, deckID string) (validate.Result, error) {
	r, err := validate.Check(ctx, t.ViteBase, t.Store.IndexPath(deckID))
	if err != nil {
		return validate.Result{OK: false, Message: err.Error()}, nil
	}
	return r, nil
}

func registerSkills(s *mcp.Server) error {
	all, err := skills.All()
	if err != nil {
		return err
	}
	for _, sk := range all {
		sk := sk
		uri := "openslide://skills/" + sk.Name
		s.AddResource(&mcp.Resource{URI: uri, Name: sk.Name, Description: sk.Description, MIMEType: "text/markdown"},
			func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "text/markdown", Text: sk.Render(true)}}}, nil
			})
	}
	s.AddPrompt(&mcp.Prompt{
		Name:        "create-slide",
		Description: "Author a new deck for a thread: loads the create-slide workflow and the slide-authoring reference.",
		Arguments: []*mcp.PromptArgument{
			{Name: "thread_id", Description: "thread or artifact id that owns the deck", Required: true},
			{Name: "topic", Description: "what the deck is about", Required: false},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		cs, err := skills.Get("create-slide")
		if err != nil {
			return nil, err
		}
		sa, err := skills.Get("slide-authoring")
		if err != nil {
			return nil, err
		}
		args := req.Params.Arguments
		text := fmt.Sprintf("Create a deck for thread_id=%q about: %s\n\n%s\n\n---\n\n%s", args["thread_id"], args["topic"], cs.Render(false), sa.Render(true))
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}}}, nil
	})
	return nil
}
