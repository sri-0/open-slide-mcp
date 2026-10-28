// Package config loads runtime settings from the environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable for the wrapper. All values come from the
// environment so the same binary runs locally and inside the container.
type Config struct {
	ListenAddr   string // address the wrapper listens on (deck origins + MCP)
	MCPPath      string // path the streamable HTTP MCP endpoint is mounted at
	Transport    string // "http" (default) or "stdio" for the MCP side
	WorkspaceDir string // workspace holding slides/ and the metadata index

	// MCPAuth is "oidc" (tool calls need a bearer token from OIDCIssuer for
	// OIDCAudience, and X-Root-Thread-ID names the owning thread) or "none"
	// (the endpoint is open and ownership is whatever the caller claims).
	MCPAuth       string
	MCPToolsOff   []string // MCP tools that exist but are not offered, e.g. deck_delete
	OIDCIssuer    string   // the iss claim tokens must carry
	OIDCDiscovery string   // where provider metadata is fetched when it is not reachable at OIDCIssuer
	OIDCAudience  string   // the aud claim tokens must contain

	// DeckOrigin is where decks are served, with {deck} as the first host
	// label (https://{deck}.decks.example.com: one origin per deck) or as
	// the path (https://decks.example.com/{deck}: one shared origin).
	DeckOrigin string
	ShellDir   string // the built editor shell: index.html and assets/
	ModulesDir string // node_modules directory the bundled deck imports come from

	ImportsExternal  []string      // deck imports resolved in the browser through the shell's import map
	ImportsBundled   []string      // deck imports bundled from ModulesDir
	CompileTimeout   time.Duration // deadline for one deck compile
	CompileMaxBytes  int64         // largest deck source accepted
	CompileMaxMemory int64         // address-space limit of the compile child, in bytes

	SidecarDir  string // built editing sidecar; empty disables the editor's save routes
	SidecarAddr string // loopback address the sidecar listens on

	PreviewSecret []byte        // HMAC key for deck tokens
	PreviewTTL    time.Duration // lifetime of a preview link
	SessionTTL    time.Duration // lifetime of the cookie a preview link is exchanged for
	DeckTTL       time.Duration // decks idle longer than this are swept; 0, the default, keeps them
	SweepInterval time.Duration // how often the sweeper runs
	EmbedOrigins  []string      // origins allowed to embed decks and set the theme; empty allows any
	LogLevel      string
}

// FromEnv reads the configuration, applying defaults and validating paths.
func FromEnv() (*Config, error) {
	c := &Config{
		ListenAddr:       getenv("LISTEN_ADDR", ":8090"),
		MCPPath:          getenv("MCP_PATH", "/mcp"),
		Transport:        getenv("MCP_TRANSPORT", "http"),
		WorkspaceDir:     getenv("WORKSPACE_DIR", "./workspace"),
		MCPAuth:          getenv("MCP_AUTH", "none"),
		MCPToolsOff:      getenvList("MCP_TOOLS_DISABLED", "deck_delete"),
		OIDCIssuer:       os.Getenv("OIDC_ISSUER_URL"),
		OIDCDiscovery:    os.Getenv("OIDC_DISCOVERY_URL"),
		OIDCAudience:     os.Getenv("OIDC_AUDIENCE"),
		DeckOrigin:       getenv("DECK_ORIGIN", "http://{deck}.decks.localhost:8090"),
		ShellDir:         getenv("SHELL_DIR", "./shell/dist"),
		ModulesDir:       getenv("MODULES_DIR", "./shell/node_modules"),
		ImportsExternal:  getenvList("DECK_IMPORTS_EXTERNAL", "react,react/jsx-runtime,react/jsx-dev-runtime,@open-slide/core"),
		ImportsBundled:   getenvList("DECK_IMPORTS_BUNDLED", "lucide-react"),
		CompileTimeout:   getenvDuration("COMPILE_TIMEOUT", 10*time.Second),
		CompileMaxBytes:  int64(getenvInt("COMPILE_MAX_BYTES", 256<<10)),
		CompileMaxMemory: int64(getenvInt("COMPILE_MAX_MEMORY", 1<<30)),
		SidecarDir:       os.Getenv("SIDECAR_DIR"),
		SidecarAddr:      getenv("SIDECAR_ADDR", "127.0.0.1:8091"),
		PreviewTTL:       getenvDuration("PREVIEW_TTL", 12*time.Hour),
		SessionTTL:       getenvDuration("SESSION_TTL", 7*24*time.Hour),
		DeckTTL:          getenvDuration("DECK_TTL", 0),
		SweepInterval:    getenvDuration("SWEEP_INTERVAL", 10*time.Minute),
		EmbedOrigins:     getenvList("EMBED_ORIGINS", ""),
		LogLevel:         getenv("LOG_LEVEL", "info"),
	}
	return c, c.check()
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
	for _, p := range c.ImportsBundled {
		if _, err := os.Stat(filepath.Join(c.ModulesDir, p)); err != nil {
			return fmt.Errorf("DECK_IMPORTS_BUNDLED: %s is not installed under MODULES_DIR %s (run make shell)", p, c.ModulesDir)
		}
	}
	if c.CompileTimeout <= 0 || c.CompileMaxBytes <= 0 {
		return errors.New("COMPILE_TIMEOUT and COMPILE_MAX_BYTES must be positive")
	}
	if c.PreviewTTL <= 0 || c.SessionTTL <= 0 {
		return errors.New("PREVIEW_TTL and SESSION_TTL must be positive")
	}
	if c.SidecarDir != "" {
		if c.SidecarDir, err = filepath.Abs(c.SidecarDir); err != nil {
			return fmt.Errorf("SIDECAR_DIR: %w", err)
		}
		if _, err := os.Stat(filepath.Join(c.SidecarDir, "server.js")); err != nil {
			return fmt.Errorf("SIDECAR_DIR %s has no server.js (run make sidecar): %w", c.SidecarDir, err)
		}
	}
	if s := os.Getenv("PREVIEW_SECRET"); s != "" {
		if len(s) < 32 {
			return errors.New("PREVIEW_SECRET must be at least 32 characters")
		}
		c.PreviewSecret = []byte(s)
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		c.PreviewSecret = []byte(hex.EncodeToString(b))
	}
	return nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// getenvList splits a comma-separated variable, dropping blanks and trailing slashes.
func getenvList(k, def string) []string {
	var out []string
	for _, v := range strings.Split(getenv(k, def), ",") {
		if v = strings.TrimRight(strings.TrimSpace(v), "/"); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
