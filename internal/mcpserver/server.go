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

	"github.com/sri-0/open-slide-mcp/internal/build"
	"github.com/sri-0/open-slide-mcp/internal/skills"
	"github.com/sri-0/open-slide-mcp/internal/workspace"
	"github.com/sri-0/open-slide-mcp/pkg/compile"
)

// Deps are the collaborators the tools need.
type Deps struct {
	Store      *workspace.Store
	Build      build.Deps
	PreviewURL func(deckID string) (string, time.Time)
	Disabled   []string // tool names registered nowhere, e.g. deck_delete
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
	s.RemoveTools(d.Disabled...)
	if err := registerSkills(s); err != nil {
		return nil, err
	}
	return s, nil
}

const instructions = `open-slide-mcp manages slide decks built with the open-slide framework (React pages on a 1920x1080 canvas).
Workflow: skill_read("slide-authoring") once per session, then deck_create once per deck (thread_id is mandatory), then deck_write with the full TSX module: every slide of the deck lives in that one module, so add or change slides by rewriting it, never by creating another deck. Fix any validation errors with another deck_write and hand the user the preview_url. Users can edit slides in the preview UI; always deck_read before rewriting and pass expected_hash so their edits are not lost. Decks are never deleted by the agent; thread_cleanup exists only for when the user asks to discard the conversation's decks.`

const (
	descCreate   = "Create ONE new deck for a request. thread_id is REQUIRED: the id of this conversation's thread exactly as given to you (your platform may fill it in); never an artifact id or a made-up value. title is human readable. Pass tsx to seed the deck, otherwise a starter cover page is written and you replace it with deck_write. Returns deck_id, content hash, created_at (use it for meta.createdAt) and preview_url. Do not create a second deck for the same request: change the deck with deck_write."
	descWrite    = "Replace a deck's index.tsx with the full TSX module. Pass expected_hash from the last deck_read or deck_write; the call fails with a conflict if the user edited the deck in the browser since then. The module is compiled on the server; it may import only react, @open-slide/core and lucide-react (no relative imports, no assets). Compile errors come back with line and column and the write is kept so you can fix incrementally; the preview keeps showing the last good version until a write compiles."
	descRead     = "Read a deck: current TSX, its hash, metadata, and any @slide-comment markers the user left in the inspector (apply them with the apply-comments skill)."
	descList     = "List the decks owned by this conversation's thread_id."
	descValidate = "Compile a deck's current source without changing it or the preview. Detects syntax errors and disallowed imports, not type errors."
	descPreview  = "Return a fresh preview_url for a deck, on the deck's own origin. Embed or open it in a browser; it renders the live deck with the open-slide editor and export controls. The link expires after PREVIEW_TTL and can be opened until then; a browser that opened it keeps access for the session lifetime."
	descCurrent  = "Return the live cursor the editor reports: which page and inspector-selected element the user is looking at. Pass deck_id for one deck, or thread_id for the newest cursor among a thread's decks. Re-read it every time the user says 'this slide' or 'this element'."
	descDelete   = "Delete one deck permanently. Only when the user explicitly asks to remove that deck; never to start over or fix a mistake, which deck_write does."
	descCleanup  = "Delete every deck owned by a thread_id. Only when the user explicitly asks to discard this conversation's decks; never to start over."
	descSkill    = "Read an open-slide authoring skill adapted for MCP use. Names: slide-authoring (technical reference, read first), create-slide (workflow), apply-comments, create-theme, current-slide. Set with_references=true to include the reference docs (transitions, steps, morph, webfonts, assets, design-system, page-numbers)."
)

type tools struct{ Deps }

type deckOut struct {
	DeckID     string         `json:"deck_id"`
	Title      string         `json:"title"`
	Hash       string         `json:"hash"`
	CreatedAt  string         `json:"created_at"`
	PreviewURL string         `json:"preview_url,omitempty"`
	Validation compile.Result `json:"validation"`
}

type createIn struct {
	ThreadID string `json:"thread_id" jsonschema:"REQUIRED. The chat thread or artifact id that owns this deck"`
	Title    string `json:"title" jsonschema:"human readable deck title"`
	TSX      string `json:"tsx,omitempty" jsonschema:"optional full index.tsx module to seed the deck"`
}

// ErrWrongThread is returned when a caller names a thread other than its own.
var ErrWrongThread = errors.New("thread_id does not match the caller's thread")

// owner returns the thread a call acts for: the X-Root-Thread-ID header the
// authenticated caller's gateway sets, or, when the endpoint runs without
// authentication, the thread_id argument the caller supplies.
func owner(req *mcp.CallToolRequest, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if req != nil && req.Extra != nil {
		if h := strings.TrimSpace(req.Extra.Header.Get("X-Root-Thread-ID")); h != "" {
			if arg != "" && arg != h {
				return "", ErrWrongThread
			}
			return h, nil
		}
	}
	return arg, nil
}

// deck loads a deck the caller may touch. When the call names an owner the
// deck must belong to it; another thread's deck is reported as not found.
func (t *tools) deck(req *mcp.CallToolRequest, id string) (*workspace.Deck, error) {
	d, err := t.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if o, _ := owner(req, ""); o != "" && d.ThreadID != o {
		return nil, workspace.ErrNotFound
	}
	return d, nil
}

func (t *tools) create(ctx context.Context, req *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, deckOut, error) {
	thread, err := owner(req, in.ThreadID)
	if err != nil {
		return nil, deckOut{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, deckOut{}, errors.New("title is required")
	}
	now := time.Now().UTC()
	tsx := in.TSX
	if tsx == "" {
		tsx = workspace.StarterTSX(in.Title, now)
	}
	deck, hash, err := t.Store.Create(thread, in.Title, tsx)
	if err != nil {
		return nil, deckOut{}, err
	}
	url, _ := t.PreviewURL(deck.ID)
	v := t.Build.Deck(ctx, deck.ID, tsx)
	t.Log.Info("deck created", "deck", deck.ID, "thread", deck.ThreadID, "valid", v.OK)
	return nil, deckOut{DeckID: deck.ID, Title: deck.Title, Hash: hash, CreatedAt: deck.CreatedAt.Format(time.RFC3339), PreviewURL: url, Validation: v}, nil
}

type writeIn struct {
	DeckID       string `json:"deck_id"`
	TSX          string `json:"tsx" jsonschema:"the complete index.tsx module"`
	ExpectedHash string `json:"expected_hash,omitempty" jsonschema:"hash from the last deck_read or deck_write; protects browser edits"`
}

func (t *tools) write(ctx context.Context, req *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, deckOut, error) {
	if strings.TrimSpace(in.TSX) == "" {
		return nil, deckOut{}, errors.New("tsx is required")
	}
	if _, err := t.deck(req, in.DeckID); err != nil {
		return nil, deckOut{}, err
	}
	deck, hash, err := t.Store.Write(in.DeckID, in.TSX, in.ExpectedHash)
	if err != nil {
		return nil, deckOut{}, err
	}
	v := t.Build.Deck(ctx, deck.ID, in.TSX)
	url, _ := t.PreviewURL(deck.ID)
	t.Log.Info("deck written", "deck", deck.ID, "valid", v.OK)
	return nil, deckOut{DeckID: deck.ID, Title: deck.Title, Hash: hash, CreatedAt: deck.CreatedAt.Format(time.RFC3339), PreviewURL: url, Validation: v}, nil
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

func (t *tools) read(_ context.Context, req *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, readOut, error) {
	if _, err := t.deck(req, in.DeckID); err != nil {
		return nil, readOut{}, err
	}
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

func (t *tools) list(_ context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	thread, err := owner(req, in.ThreadID)
	if err != nil {
		return nil, listOut{}, err
	}
	if thread == "" {
		return nil, listOut{}, workspace.ErrBadThread
	}
	decks := t.Store.List(thread)
	if decks == nil {
		decks = []*workspace.Deck{}
	}
	return nil, listOut{Decks: decks}, nil
}

func (t *tools) validate(ctx context.Context, req *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, compile.Result, error) {
	if _, err := t.deck(req, in.DeckID); err != nil {
		return nil, compile.Result{}, err
	}
	_, src, _, _, err := t.Store.Read(in.DeckID)
	if err != nil {
		return nil, compile.Result{}, err
	}
	r, err := t.Build.Compiler.Compile(ctx, []byte(src))
	if err != nil {
		return nil, compile.Result{OK: false, Message: err.Error()}, nil
	}
	r.JS, r.Map = nil, nil
	return nil, r, nil
}

type previewOut struct {
	DeckID     string `json:"deck_id"`
	Title      string `json:"title"`
	PreviewURL string `json:"preview_url"`
	ExpiresAt  string `json:"expires_at"`
}

func (t *tools) preview(_ context.Context, req *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, previewOut, error) {
	deck, err := t.deck(req, in.DeckID)
	if err != nil {
		return nil, previewOut{}, err
	}
	url, exp := t.PreviewURL(in.DeckID)
	return nil, previewOut{DeckID: deck.ID, Title: deck.Title, PreviewURL: url, ExpiresAt: exp.UTC().Format(time.RFC3339)}, nil
}

type currentIn struct {
	DeckID   string `json:"deck_id,omitempty" jsonschema:"one deck"`
	ThreadID string `json:"thread_id,omitempty" jsonschema:"the newest cursor among this thread's decks"`
}

type currentOut struct {
	Current *workspace.Current `json:"current,omitempty"`
	Note    string             `json:"note,omitempty"`
}

func (t *tools) current(_ context.Context, req *mcp.CallToolRequest, in currentIn) (*mcp.CallToolResult, currentOut, error) {
	var ids []string
	thread, err := owner(req, in.ThreadID)
	if err != nil {
		return nil, currentOut{}, err
	}
	switch {
	case in.DeckID != "":
		if _, err := t.deck(req, in.DeckID); err != nil {
			return nil, currentOut{}, err
		}
		ids = []string{in.DeckID}
	case thread != "":
		for _, d := range t.Store.List(thread) {
			ids = append(ids, d.ID)
		}
	default:
		return nil, currentOut{}, errors.New("deck_id or thread_id is required")
	}
	c := t.Store.ReadCurrent(ids...)
	if c == nil {
		return nil, currentOut{Note: "no cursor yet; the user has not opened the deck in the preview"}, nil
	}
	return nil, currentOut{Current: c}, nil
}

type deleteOut struct {
	Deleted []string `json:"deleted"`
}

func (t *tools) del(_ context.Context, req *mcp.CallToolRequest, in deckIn) (*mcp.CallToolResult, deleteOut, error) {
	if _, err := t.deck(req, in.DeckID); err != nil {
		return nil, deleteOut{}, err
	}
	if err := t.Store.Delete(in.DeckID); err != nil {
		return nil, deleteOut{}, err
	}
	t.Log.Info("deck deleted", "deck", in.DeckID)
	return nil, deleteOut{Deleted: []string{in.DeckID}}, nil
}

type cleanupIn struct {
	ThreadID string `json:"thread_id"`
}

func (t *tools) cleanup(_ context.Context, req *mcp.CallToolRequest, in cleanupIn) (*mcp.CallToolResult, deleteOut, error) {
	thread, err := owner(req, in.ThreadID)
	if err != nil {
		return nil, deleteOut{}, err
	}
	if thread == "" {
		return nil, deleteOut{}, workspace.ErrBadThread
	}
	ids, err := t.Store.DeleteThread(thread)
	if ids == nil {
		ids = []string{}
	}
	t.Log.Info("thread cleaned", "thread", thread, "deleted", len(ids))
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
