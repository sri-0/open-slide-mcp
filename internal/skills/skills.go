// Package skills embeds the open-slide authoring skills and adapts them for
// use through the MCP tools rather than a local filesystem.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed data
var data embed.FS

// Skill is one embedded skill with its optional reference documents.
type Skill struct {
	Name        string
	Description string
	Body        string
	References  map[string]string
}

// Preface is prepended to every skill so the model knows how the filesystem
// instructions map onto MCP tools.
const Preface = `# How this skill applies through the open-slide MCP

You are not working in a local checkout. Every deck lives on the MCP server.
Translate the skill's filesystem instructions as follows:

- "write slides/<id>/index.tsx" means call deck_write with the full TSX and the deck_id.
- "read slides/<id>/index.tsx" means call deck_read.
- "list slides/" means call deck_list with your thread_id.
- To start a new deck call deck_create with thread_id (required), a short kebab-case name and a title.
- "themes/" is empty unless the operator added themes; skip theme selection if deck_list_themes is not available.
- Do not run shell commands for timestamps. deck_create returns created_at; reuse it for meta.createdAt.
- A deck may import only react, @open-slide/core and lucide-react (icons: import { Rocket } from 'lucide-react'). Relative imports and asset files are refused by the compiler: use inline SVG, CSS, or ImagePlaceholder instead of ./assets.
- External web fonts are blocked by the deck's content security policy. Use the design-system fonts (var(--osd-font-*)), Geist, or system font stacks; skip the webfonts reference.
- If AskUserQuestion is unavailable, ask in plain chat, or make sensible assumptions and state them.
- deck_write compiles the module on the server and returns errors with line and column. Fix and rewrite until it passes; the preview keeps the last good version meanwhile.
- After writing, tell the user the preview_url returned by deck_create or deck_preview_url.
- "Previewing a page visually": there is no server.json to read. If you have a browser tool, open the preview_url once, then /s/<deck_id>/preview?p=N on the same origin; otherwise skip the visual check.

`

// All returns every embedded skill sorted by name.
func All() ([]*Skill, error) {
	entries, err := fs.ReadDir(data, "data")
	if err != nil {
		return nil, err
	}
	var out []*Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := Get(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get loads one skill by directory name.
func Get(name string) (*Skill, error) {
	name = path.Clean(name)
	if strings.Contains(name, "/") || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	raw, err := data.ReadFile(path.Join("data", name, "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("unknown skill %q", name)
	}
	s := &Skill{Name: name, References: map[string]string{}}
	s.Description, s.Body = splitFrontmatter(string(raw))
	refs, _ := fs.ReadDir(data, path.Join("data", name, "references"))
	for _, r := range refs {
		if r.IsDir() {
			continue
		}
		b, err := data.ReadFile(path.Join("data", name, "references", r.Name()))
		if err != nil {
			return nil, err
		}
		s.References[r.Name()] = string(b)
	}
	return s, nil
}

// Render returns the skill body with the MCP preface, and optionally the
// reference documents appended.
func (s *Skill) Render(withRefs bool) string {
	var b strings.Builder
	b.WriteString(Preface)
	b.WriteString(s.Body)
	if withRefs && len(s.References) > 0 {
		names := make([]string, 0, len(s.References))
		for n := range s.References {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "\n\n---\n\n# Reference: %s\n\n%s", n, s.References[n])
		}
	}
	return b.String()
}

func splitFrontmatter(raw string) (description, body string) {
	if !strings.HasPrefix(raw, "---\n") {
		return "", raw
	}
	rest := raw[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", raw
	}
	fm := rest[:end]
	body = strings.TrimLeft(rest[end+5:], "\n")
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return description, body
}
