package editor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sri-0/open-slide-mcp/pkg/edits"
)

// Sidecar is the Node engine: a parse-only process that it runs and
// restarts, started under Node's permission model with read access to its
// own bundle only and an environment of nothing but PATH and its listen
// address, so neither the deck source it handles nor the wrapper's secrets
// can reach anything else. It applies requests over loopback HTTP.
type Sidecar struct {
	Dir    string // directory holding server.js
	Addr   string // loopback address to listen on
	Log    *slog.Logger
	client http.Client
}

// NewSidecar returns a Sidecar with a bounded client for its loopback port.
func NewSidecar(dir, addr string, log *slog.Logger) *Sidecar {
	return &Sidecar{Dir: dir, Addr: addr, Log: log, client: http.Client{Timeout: 15 * time.Second}}
}

// Apply posts the request to the sidecar's /apply.
func (s *Sidecar) Apply(ctx context.Context, in edits.Request) (edits.Response, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return edits.Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+s.Addr+"/apply", bytes.NewReader(body))
	if err != nil {
		return edits.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return edits.Response{}, fmt.Errorf("sidecar: %w", err)
	}
	defer resp.Body.Close()
	var out edits.Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		return edits.Response{}, fmt.Errorf("sidecar returned %d: %w", resp.StatusCode, errors.Join(err, errors.New("unreadable response")))
	}
	return out, nil
}

// WaitReady polls the sidecar until it answers or the deadline passes.
func (s *Sidecar) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.Addr+"/healthz", nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("sidecar did not become ready at " + s.Addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Run blocks until ctx is cancelled, restarting the process with backoff
// whenever it exits.
func (s *Sidecar) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		start := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		s.Log.Error("sidecar exited", "err", err)
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (s *Sidecar) runOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "node", "--permission", "--allow-fs-read="+s.Dir, filepath.Join(s.Dir, "server.js"))
	cmd.Dir = s.Dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SIDECAR_ADDR=" + s.Addr, "NO_COLOR=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	s.Log.Info("sidecar started", "pid", cmd.Process.Pid, "addr", s.Addr)
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			s.Log.Debug("sidecar", "line", line)
		}
	}
	return cmd.Wait()
}
