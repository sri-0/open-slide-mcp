// Command open-slide-mcp serves slide decks as MCP tools for agents and as
// the open-slide editor for people. Every deck is compiled on its own by
// esbuild and served on its own origin; nothing an agent writes is ever
// executed on the server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sri-0/open-slide-mcp/internal/build"
	"github.com/sri-0/open-slide-mcp/internal/config"
	"github.com/sri-0/open-slide-mcp/internal/editor"
	"github.com/sri-0/open-slide-mcp/internal/mcpserver"
	"github.com/sri-0/open-slide-mcp/internal/shell"
	"github.com/sri-0/open-slide-mcp/internal/workspace"
	"github.com/sri-0/open-slide-mcp/pkg/auth"
	"github.com/sri-0/open-slide-mcp/pkg/compile"
	"github.com/sri-0/open-slide-mcp/pkg/events"
)

var version = "dev"

func main() {
	// The server re-executes itself as `open-slide-mcp compile` for every
	// deck, so a pathological deck can only take down that child.
	if len(os.Args) > 1 && os.Args[1] == "compile" {
		if err := compile.Child(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := config.FromEnv(ctx)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	store, err := workspace.Open(cfg.WorkspaceDir)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// The shell build lists the names each import-map entry exports.
	var exports map[string][]string
	if b, err := os.ReadFile(filepath.Join(cfg.ShellDir, "exports.json")); err != nil {
		return fmt.Errorf("shell exports: %w", err)
	} else if err := json.Unmarshal(b, &exports); err != nil {
		return fmt.Errorf("shell exports: %w", err)
	}
	comp, err := compile.New(compile.Config{
		Self: self, External: cfg.ImportsExternal, Bundled: cfg.ImportsBundled, ModulesDir: cfg.ModulesDir, Exports: exports,
		Timeout: cfg.CompileTimeout, MaxBytes: cfg.CompileMaxBytes, MaxMemory: cfg.CompileMaxMemory,
	})
	if err != nil {
		return err
	}
	signer, err := auth.New([]byte(cfg.PreviewSecret))
	if err != nil {
		return err
	}
	origins, err := shell.ParseOrigins(cfg.DeckOrigin)
	if err != nil {
		return err
	}
	broker := events.New()
	deps := build.Builder{Store: store, Compiler: comp, Events: broker}

	var ed http.Handler
	if cfg.SidecarDir != "" {
		sc := editor.NewSidecar(cfg.SidecarDir, cfg.SidecarAddr, log)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := sc.Run(ctx); err != nil {
				log.Error("sidecar supervisor stopped", "err", err)
			}
		}()
		defer func() {
			stop()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				log.Warn("sidecar did not stop within 10s")
			}
		}()
		if err := sc.WaitReady(ctx, 30*time.Second); err != nil {
			return err
		}
		ed = &editor.Editor{Build: builder, Apply: sc, Log: log}
		log.Info("editing enabled", "sidecar", cfg.SidecarAddr)
	} else {
		log.Warn("SIDECAR_DIR is empty: decks are viewable but browser edits are off")
	}

	sh, err := shell.New(shell.Server{
		Origins: origins, Dir: cfg.ShellDir, Signer: signer, Cookie: "__Secure-" + cfg.CookieName, LinkTTL: cfg.PreviewTTL, SessionTTL: cfg.SessionTTL,
		Store: store, Events: broker, Editor: ed, EmbedOrigins: cfg.EmbedOrigins, Log: log,
	})
	if err != nil {
		return err
	}
	if len(cfg.EmbedOrigins) == 0 {
		log.Warn("EMBED_ORIGINS is empty: any site may embed decks and set their theme")
	}

	srv, err := mcpserver.New(mcpserver.Deps{Store: store, Build: builder, PreviewURL: sh.PreviewURL, ToolsDisabled: cfg.ToolsDisabled, Log: log, Version: version})
	if err != nil {
		return err
	}

	go sweeper(ctx, store, cfg, log)

	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		// The path only: a preview link carries its token in the query.
		LogURIPath: true, LogStatus: true, LogMethod: true, LogLatency: true, LogHost: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			log.Debug("http", "method", v.Method, "host", v.Host, "path", v.URIPath, "status", v.Status, "latency", v.Latency)
			return nil
		},
	}))
	e.Pre(sh.Middleware())
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "version": version})
	})
	if cfg.Transport == "http" {
		var bearer auth.Bearer
		if cfg.MCPAuth == "oidc" {
			if bearer.Verify, err = auth.OIDC(ctx, cfg.OIDCIssuer, cfg.OIDCDiscovery, cfg.OIDCAudience); err != nil {
				return err
			}
			log.Info("mcp auth", "issuer", cfg.OIDCIssuer, "audience", cfg.OIDCAudience)
		} else {
			log.Warn("MCP_AUTH is none: the MCP endpoint is open and deck ownership is whatever the caller claims")
		}
		h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
		e.Any(cfg.MCPPath, echo.WrapHandler(bearer.Wrap(h)))
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Shutdown(shutdown)
	}()

	if cfg.Transport == "stdio" {
		go func() {
			if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
				log.Error("stdio transport ended", "err", err)
			}
			stop()
		}()
	}

	log.Info("listening", "addr", cfg.ListenAddr, "mcp", cfg.MCPPath, "mcp_auth", cfg.MCPAuth, "tools_disabled", cfg.ToolsDisabled, "deck_origin", cfg.DeckOrigin, "embed_origins", cfg.EmbedOrigins, "transport", cfg.Transport)
	if err := e.Start(cfg.ListenAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func sweeper(ctx context.Context, store *workspace.Store, cfg *config.Config, log *slog.Logger) {
	if cfg.DeckTTL <= 0 {
		return
	}
	t := time.NewTicker(cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ids, err := store.Sweep(cfg.DeckTTL)
			if err != nil {
				log.Error("sweep", "err", err)
			}
			if len(ids) > 0 {
				log.Info("swept idle decks", "ids", ids)
			}
		}
	}
}
