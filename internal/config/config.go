// Package config loads runtime settings from the environment.
package config

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"
)

// Config holds every tunable for the wrapper. All values come from the
// environment so the same binary runs locally and inside the container.
type Config struct {
	ListenAddr   string `env:"LISTEN_ADDR,default=:8090"`         // address for the MCP endpoint and every deck origin
	MCPPath      string `env:"MCP_PATH,default=/mcp"`             // path the streamable HTTP MCP endpoint is mounted at
	Transport    string `env:"MCP_TRANSPORT,default=http"`        // "http" or "stdio" for the MCP side
	WorkspaceDir string `env:"WORKSPACE_DIR,default=./workspace"` // holds slides/, compiled builds and the metadata index

	// MCPAuth is "oidc" (tool calls need a bearer token from OIDCIssuer for
	// OIDCAudience, and X-Root-Thread-ID names the owning thread) or "none"
	// (the endpoint is open and ownership is whatever the caller claims).
	MCPAuth       string   `env:"MCP_AUTH,default=none"`
	MCPToolsOff   []string `env:"MCP_TOOLS_DISABLED,default=deck_delete"` // tools that exist but are not offered
	OIDCIssuer    string   `env:"OIDC_ISSUER_URL"`                        // the iss claim tokens must carry
	OIDCDiscovery string   `env:"OIDC_DISCOVERY_URL"`                     // where provider metadata is fetched when the issuer is not reachable by that name
	OIDCAudience  string   `env:"OIDC_AUDIENCE"`                          // the aud claim tokens must contain

	// DeckOrigin is where decks are served, with {deck} as the first host
	// label (https://{deck}.decks.example.com: one origin per deck) or as
	// the path (https://decks.example.com/{deck}: one shared origin).
	DeckOrigin string `env:"DECK_ORIGIN,default=http://{deck}.decks.localhost:8090"`
	ShellDir   string `env:"SHELL_DIR,default=./shell/dist"`           // the built editor shell: index.html and assets/
	ModulesDir string `env:"MODULES_DIR,default=./shell/node_modules"` // node_modules directory the bundled deck imports come from
	CookieName string `env:"COOKIE_NAME,default=osmcp"`                // the deck session cookie, behind the __Secure- prefix

	ImportsExternal  []string      `env:"DECK_IMPORTS_EXTERNAL"`                 // deck imports resolved in the browser through the shell's import map
	ImportsBundled   []string      `env:"DECK_IMPORTS_BUNDLED"`                  // deck imports bundled from ModulesDir
	CompileTimeout   time.Duration `env:"COMPILE_TIMEOUT,default=10s"`           // deadline for one deck compile
	CompileMaxBytes  int64         `env:"COMPILE_MAX_BYTES,default=262144"`      // largest deck source accepted
	CompileMaxMemory int64         `env:"COMPILE_MAX_MEMORY,default=1073741824"` // address-space limit of the compile child, in bytes

	SidecarDir  string `env:"SIDECAR_DIR"`                         // built editing sidecar; empty disables the editor's save routes
	SidecarAddr string `env:"SIDECAR_ADDR,default=127.0.0.1:8091"` // loopback address the sidecar listens on

	PreviewSecret string        `env:"PREVIEW_SECRET"`             // HMAC key for deck tokens; random when unset
	PreviewTTL    time.Duration `env:"PREVIEW_TTL,default=12h"`    // lifetime of a preview link
	SessionTTL    time.Duration `env:"SESSION_TTL,default=168h"`   // lifetime of the cookie a preview link is exchanged for
	DeckTTL       time.Duration `env:"DECK_TTL,default=0"`         // decks idle longer than this are swept; 0 keeps them
	SweepInterval time.Duration `env:"SWEEP_INTERVAL,default=10m"` // how often the sweeper runs
	EmbedOrigins  []string      `env:"EMBED_ORIGINS"`              // origins allowed to embed decks and set the theme; empty allows any
	LogLevel      string        `env:"LOG_LEVEL,default=info"`
}

// FromEnv reads the configuration, applying defaults and validating paths.
func FromEnv(ctx context.Context) (*Config, error) {
	var c Config
	if err := envconfig.Process(ctx, &c); err != nil {
		return nil, err
	}
	return &c, c.check()
}

// check fills the derived values and fails with what to set.
func (c *Config) check() error {
	var err error
	if c.WorkspaceDir, err = filepath.Abs(c.WorkspaceDir); err != nil {
		return fmt.Errorf("WORKSPACE_DIR: %w", err)
	}
	if c.Transport != "http" && c.Transport != "stdio" {
		return fmt.Errorf("MCP_TRANSPORT must be http or stdio, got %q", c.Transport)
	}
	switch c.MCPAuth {
	case "none":
	case "oidc":
		if c.OIDCIssuer == "" || c.OIDCAudience == "" {
			return errors.New("MCP_AUTH=oidc needs OIDC_ISSUER_URL and OIDC_AUDIENCE")
		}
	default:
		return fmt.Errorf("MCP_AUTH must be oidc or none, got %q", c.MCPAuth)
	}
	// url.Parse rejects braces in a host, so the label is stood in for.
	u, err := url.Parse(strings.Replace(c.DeckOrigin, "{deck}", "deck", 1))
	hostMode := strings.Contains(c.DeckOrigin, "://{deck}.") && u != nil && u.Path == ""
	pathMode := strings.HasSuffix(c.DeckOrigin, "/{deck}") && u != nil && u.Path == "/deck"
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !(hostMode || pathMode) {
		return errors.New("DECK_ORIGIN must be http(s)://{deck}.<domain>[:port] (one origin per deck) or http(s)://<host>[:port]/{deck} (one shared origin)")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && !strings.HasSuffix(u.Hostname(), ".localhost") {
		return errors.New("DECK_ORIGIN must be https unless the host is localhost or ends in .localhost: deck cookies are Secure")
	}
	if c.ShellDir, err = filepath.Abs(c.ShellDir); err != nil {
		return fmt.Errorf("SHELL_DIR: %w", err)
	}
	if _, err := os.Stat(filepath.Join(c.ShellDir, "index.html")); err != nil {
		return fmt.Errorf("SHELL_DIR %s has no index.html (run make shell): %w", c.ShellDir, err)
	}
	if c.ModulesDir, err = filepath.Abs(c.ModulesDir); err != nil {
		return fmt.Errorf("MODULES_DIR: %w", err)
	}
	if c.CookieName == "" || strings.ContainsAny(c.CookieName, " ;,=") {
		return errors.New("COOKIE_NAME must be a plain token")
	}
	// The default lists hold commas, which a struct tag cannot carry.
	if len(c.ImportsExternal) == 0 {
		c.ImportsExternal = []string{"react", "react/jsx-runtime", "react/jsx-dev-runtime", "@open-slide/core"}
	}
	if len(c.ImportsBundled) == 0 {
		c.ImportsBundled = []string{"lucide-react"}
	}
	for _, p := range c.ImportsBundled {
		if _, err := os.Stat(filepath.Join(c.ModulesDir, p)); err != nil {
			return fmt.Errorf("DECK_IMPORTS_BUNDLED: %s is not installed under MODULES_DIR %s (run make shell)", p, c.ModulesDir)
		}
	}
	if c.SidecarDir != "" {
		if c.SidecarDir, err = filepath.Abs(c.SidecarDir); err != nil {
			return fmt.Errorf("SIDECAR_DIR: %w", err)
		}
		if _, err := os.Stat(filepath.Join(c.SidecarDir, "server.js")); err != nil {
			return fmt.Errorf("SIDECAR_DIR %s has no server.js (run make sidecar): %w", c.SidecarDir, err)
		}
	}
	if c.CompileTimeout <= 0 || c.CompileMaxBytes <= 0 {
		return errors.New("COMPILE_TIMEOUT and COMPILE_MAX_BYTES must be positive")
	}
	if c.PreviewTTL <= 0 || c.SessionTTL <= 0 {
		return errors.New("PREVIEW_TTL and SESSION_TTL must be positive")
	}
	for i, o := range c.EmbedOrigins {
		c.EmbedOrigins[i] = strings.TrimRight(strings.TrimSpace(o), "/")
	}
	switch {
	case c.PreviewSecret == "":
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		c.PreviewSecret = hex.EncodeToString(b)
	case len(c.PreviewSecret) < 32:
		return errors.New("PREVIEW_SECRET must be at least 32 characters")
	}
	return nil
}
