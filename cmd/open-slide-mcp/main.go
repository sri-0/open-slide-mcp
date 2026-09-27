// Command open-slide-mcp wraps an open-slide workspace: it supervises the
// dev server, proxies it with preview tokens, and serves MCP tools.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sri-0/open-slide-mcp/internal/config"
	"github.com/sri-0/open-slide-mcp/internal/devserver"
	"github.com/sri-0/open-slide-mcp/internal/mcpserver"
	"github.com/sri-0/open-slide-mcp/internal/proxy"
	"github.com/sri-0/open-slide-mcp/internal/workspace"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		lvl = slog.LevelInfo
	}
	logOut := os.Stderr
	log := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := workspace.Open(cfg.WorkspaceDir)
	if err != nil {
		return err
	}

	if cfg.ManageDevServer {
		sup := &devserver.Supervisor{Dir: cfg.WorkspaceDir, Host: cfg.ViteHost, Port: cfg.VitePort, Log: log}
		go func() {
			if err := sup.Run(ctx); err != nil {
				log.Error("dev server supervisor stopped", "err", err)
			}
		}()
	}
	if err := devserver.WaitReady(ctx, cfg.ViteBase(), 60*time.Second); err != nil {
		return err
	}
	log.Info("dev server ready", "url", cfg.ViteBase())

	px, err := proxy.New(cfg.ViteBase(), cfg.PreviewSecret, cfg.PreviewAuth, cfg.PreviewTTL, cfg.PublicURL, store.Touch)
	if err != nil {
		return err
	}

	srv, err := mcpserver.New(mcpserver.Deps{Store: store, ViteBase: cfg.ViteBase(), PreviewURL: px.PreviewURL, Log: log, Version: version})
	if err != nil {
		return err
	}

	go sweeper(ctx, store, cfg, log)

	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI: true, LogStatus: true, LogMethod: true, LogLatency: true,
		Skipper: func(c echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasPrefix(p, "/@") || strings.HasPrefix(p, "/node_modules") || strings.HasPrefix(p, "/src/")
		},
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			log.Debug("http", "method", v.Method, "uri", v.URI, "status", v.Status, "latency", v.Latency)
			return nil
		},
	}))
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "version": version, "decks": len(store.List(""))})
	})
	if cfg.Transport == "http" {
		h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
			Stateless:                 true,
			DisableLocalhostProtection: true,
		})
		e.Any(cfg.MCPPath, echo.WrapHandler(h))
	}
	px.Register(e)

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

	log.Info("listening", "addr", cfg.ListenAddr, "mcp", cfg.MCPPath, "public_url", cfg.PublicURL, "preview_auth", cfg.PreviewAuth, "transport", cfg.Transport)
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
