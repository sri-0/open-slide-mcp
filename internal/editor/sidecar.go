package editor

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Sidecar runs the parse-only Node process and restarts it when it exits.
// It is started under Node's permission model with read access to its own
// bundle only, and with an environment that holds nothing but PATH and its
// listen address, so neither the deck source it handles nor the wrapper's
// secrets can reach anything else.
type Sidecar struct {
	Dir  string // directory holding server.js
	Addr string // loopback address to listen on
	Log  *slog.Logger
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
	cmd.Stdin = nil
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
