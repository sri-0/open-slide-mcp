// Package devserver spawns and supervises the open-slide dev server.
package devserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Supervisor keeps one `open-slide dev` process alive.
type Supervisor struct {
	Dir  string
	Host string
	Port int
	Log  *slog.Logger
}

// Run blocks until ctx is cancelled, restarting the dev server with backoff
// whenever it exits.
func (s *Supervisor) Run(ctx context.Context) error {
	bin := filepath.Join(s.Dir, "node_modules", ".bin", "open-slide")
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("open-slide binary missing at %s; run pnpm install in the workspace", bin)
	}
	backoff := time.Second
	for {
		start := time.Now()
		err := s.runOnce(ctx, bin)
		if ctx.Err() != nil {
			return nil
		}
		s.Log.Error("dev server exited", "err", err)
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

func (s *Supervisor) runOnce(ctx context.Context, bin string) error {
	cmd := exec.CommandContext(ctx, bin, "dev", "--host", s.Host, "-p", strconv.Itoa(s.Port))
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), "FORCE_COLOR=0", "CI=1", "NO_COLOR=1")
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole process group so vite's child workers die too.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	s.Log.Info("dev server started", "pid", cmd.Process.Pid, "url", s.URL())
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			s.Log.Info("vite", "line", line)
		}
	}
	return cmd.Wait()
}

// URL returns the loopback base URL of the dev server.
func (s *Supervisor) URL() string {
	return fmt.Sprintf("http://%s:%d", s.Host, s.Port)
}

// WaitReady polls the dev server until it answers or the deadline passes.
func WaitReady(ctx context.Context, base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("dev server did not become ready at " + base)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
