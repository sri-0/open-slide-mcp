// Package compile turns one deck module into one ES module with esbuild,
// in a child process so a pathological deck can only kill the child.
//
// esbuild never evaluates its input, but it is not a sandbox: a deck of
// deeply nested expressions can take gigabytes or overflow the Go stack,
// which is fatal and cannot be recovered. Compile therefore re-executes
// this binary with the "compile" subcommand, hands it the deck on stdin
// and reads the result back from stdout, under a deadline, resource limits
// and an input size cap. The deck's bare imports of the shell's packages
// stay external for the browser's import map; icon packages are bundled in
// from a pinned copy on disk; every other import is refused.
package compile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config tunes one Compiler. New checks it and makes ModulesDir absolute.
type Config struct {
	Self       string   // path of the binary to re-exec with the "compile" subcommand (os.Executable())
	External   []string // bare imports left for the browser's import map, e.g. react, react/jsx-runtime, react/jsx-dev-runtime, @open-slide/core
	Bundled    []string // bare imports bundled from ModulesDir, e.g. lucide-react
	ModulesDir string   // a node_modules directory holding the Bundled packages (esbuild NodePaths); required when Bundled is set
	// Exports lists the names each External specifier provides, so a deck
	// importing a name the shell does not export fails here, not in every
	// viewer's browser. A specifier without an entry is not checked.
	Exports   map[string][]string
	Timeout   time.Duration // deadline for one compile
	MaxBytes  int64         // input size cap, checked in the parent before spawning
	MaxMemory int64         // RLIMIT_AS for the child, in bytes; 0 leaves it unset
}

// Result is the outcome of one compile. Its JSON shape matches
// validate.Result so MCP tool outputs do not change; the module and its
// source map travel outside JSON.
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Line    int    `json:"line,omitempty"`   // 1-based line of the first error
	Column  int    `json:"column,omitempty"` // 0-based byte offset within that line, as esbuild reports it
	JS      []byte `json:"-"`                // the ES module when OK
	Map     []byte `json:"-"`                // its external source map when OK
}

// Configuration mistakes reported by New.
var (
	ErrNoSelf     = errors.New("compile: set Self to the path of this binary (os.Executable())")
	ErrNoTimeout  = errors.New("compile: set Timeout to a positive duration")
	ErrNoMaxBytes = errors.New("compile: set MaxBytes to a positive size")
	ErrNoModules  = errors.New("compile: set ModulesDir to the node_modules directory holding the Bundled packages")
)

// request is what the parent writes to the child's stdin.
type request struct {
	Config Config `json:"config"`
	Source string `json:"source"`
}

// response is what the child writes to stdout: the Result and, when OK,
// the module and source map that Result keeps out of its own JSON.
type response struct {
	Result
	JS  []byte `json:"js,omitempty"`
	Map []byte `json:"map,omitempty"`
}

// Compiler runs esbuild in a child process. It is safe for concurrent use.
type Compiler struct {
	cfg Config
}

// New checks cfg and returns a Compiler.
func New(cfg Config) (*Compiler, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	return &Compiler{cfg: cfg}, nil
}

func (c *Config) check() error {
	switch {
	case c.Self == "":
		return ErrNoSelf
	case c.Timeout <= 0:
		return ErrNoTimeout
	case c.MaxBytes <= 0:
		return ErrNoMaxBytes
	}
	if c.ModulesDir != "" {
		abs, err := filepath.Abs(c.ModulesDir)
		if err != nil {
			return fmt.Errorf("compile: ModulesDir: %w", err)
		}
		c.ModulesDir = abs
	}
	if len(c.Bundled) > 0 {
		if info, err := os.Stat(c.ModulesDir); err != nil || !info.IsDir() {
			return fmt.Errorf("%w: %q", ErrNoModules, c.ModulesDir)
		}
	}
	return nil
}

// Compile builds src, the deck's index.tsx, into one ES module. Deck
// problems (too large, a syntax error, a refused import, a crash or
// timeout in the child) come back as a Result with OK false and a message
// naming the cause; only infrastructure problems, such as a missing
// binary, are returned as an error.
func (c *Compiler) Compile(ctx context.Context, src []byte) (Result, error) {
	if int64(len(src)) > c.cfg.MaxBytes {
		return Result{Message: fmt.Sprintf("deck is %d bytes; the limit is %d", len(src), c.cfg.MaxBytes)}, nil
	}
	req, err := json.Marshal(request{Config: c.cfg, Source: string(src)})
	if err != nil {
		return Result{}, fmt.Errorf("compile request: %w", err)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.cfg.Self, "compile")
	cmd.Stdin = bytes.NewReader(req)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// The child has no children of its own, so its pipes close when it is
	// killed; WaitDelay only bounds the wait if that ever stops being true.
	cmd.WaitDelay = c.cfg.Timeout
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("compile: %w", err)
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	switch {
	case parent.Err() != nil:
		return Result{}, parent.Err()
	case ctx.Err() != nil:
		return Result{Message: fmt.Sprintf("compile timed out after %s", c.cfg.Timeout)}, nil
	case errors.As(err, &exit):
		// A Go stack overflow prints its reason on the first line of
		// stderr; an rlimit kill prints nothing and shows in the state.
		line, _, _ := strings.Cut(stderr.String(), "\n")
		return Result{Message: strings.TrimSpace(fmt.Sprintf("compiler crashed (%s): %s", exit, line))}, nil
	case err != nil:
		return Result{}, fmt.Errorf("compile: %w", err)
	}
	var resp response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return Result{Message: fmt.Sprintf("compiler output unreadable: %v", err)}, nil
	}
	res := resp.Result
	res.JS, res.Map = resp.JS, resp.Map
	return res, nil
}
