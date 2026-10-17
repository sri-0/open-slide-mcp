// Package config loads runtime settings from the environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable for the wrapper. All values come from the
// environment so the same binary runs locally and inside the container.
type Config struct {
	ListenAddr      string        // address the wrapper listens on (proxy + MCP)
	MCPPath         string        // path the streamable HTTP MCP endpoint is mounted at
	Transport       string        // "http" (default) or "stdio" for the MCP side
	WorkspaceDir    string        // open-slide workspace containing package.json and slides/
	PublicURL       string        // externally reachable base URL of the wrapper, used in preview links
	ViteHost        string        // host the open-slide dev server binds to
	VitePort        int           // port the open-slide dev server binds to
	ManageDevServer bool          // spawn and supervise the dev server; false attaches to an existing one
	PreviewAuth     bool          // require a signed token to reach the proxied dev server
	PreviewSecret   []byte        // HMAC key for preview tokens
	PreviewTTL      time.Duration // lifetime of a preview token and its cookie
	DeckTTL         time.Duration // decks idle longer than this are swept; 0, the default, keeps them
	SweepInterval   time.Duration // how often the sweeper runs
	EmbedOrigins    []string      // origins allowed to set the theme by postMessage; empty allows any
	LogLevel        string
}

// FromEnv reads the configuration, applying defaults and validating paths.
func FromEnv() (*Config, error) {
	c := &Config{
		ListenAddr:      getenv("LISTEN_ADDR", ":8090"),
		MCPPath:         getenv("MCP_PATH", "/mcp"),
		Transport:       getenv("MCP_TRANSPORT", "http"),
		WorkspaceDir:    getenv("WORKSPACE_DIR", "./workspace"),
		PublicURL:       strings.TrimRight(getenv("PUBLIC_URL", "http://localhost:8090"), "/"),
		ViteHost:        getenv("VITE_HOST", "127.0.0.1"),
		VitePort:        getenvInt("VITE_PORT", 5173),
		ManageDevServer: getenvBool("MANAGE_DEVSERVER", true),
		PreviewAuth:     getenvBool("PREVIEW_AUTH", false),
		PreviewTTL:      getenvDuration("PREVIEW_TTL", 12*time.Hour),
		DeckTTL:         getenvDuration("DECK_TTL", 0),
		SweepInterval:   getenvDuration("SWEEP_INTERVAL", 10*time.Minute),
		EmbedOrigins:    getenvList("EMBED_ORIGINS"),
		LogLevel:        getenv("LOG_LEVEL", "info"),
	}
	abs, err := filepath.Abs(c.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("workspace dir: %w", err)
	}
	c.WorkspaceDir = abs
	if _, err := os.Stat(filepath.Join(abs, "package.json")); err != nil {
		return nil, fmt.Errorf("workspace %s has no package.json (run make setup): %w", abs, err)
	}
	if c.Transport != "http" && c.Transport != "stdio" {
		return nil, fmt.Errorf("MCP_TRANSPORT must be http or stdio, got %q", c.Transport)
	}
	if s := os.Getenv("PREVIEW_SECRET"); s != "" {
		c.PreviewSecret = []byte(s)
	} else {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		c.PreviewSecret = []byte(hex.EncodeToString(b))
	}
	return c, nil
}

// ViteBase is the loopback URL of the dev server.
func (c *Config) ViteBase() string {
	return fmt.Sprintf("http://%s:%d", c.ViteHost, c.VitePort)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// getenvList splits a comma-separated variable, dropping blanks and trailing slashes.
func getenvList(k string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(k), ",") {
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

func getenvBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
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
