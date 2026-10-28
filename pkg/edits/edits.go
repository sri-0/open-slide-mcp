// Package edits runs open-slide's editor save logic, the same JavaScript
// bundle the Node sidecar serves, inside the Go process with goja, a
// JavaScript interpreter written in Go. The deck source is only ever data
// to it: the bundle parses the text with Babel and splices the requested
// change, and a parse that runs too long or too deep is cut off from Go.
package edits

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
	"github.com/google/uuid"
)

// bundle is edits/src/embedded/entry.ts built by esbuild (make edits); it
// defines a global apply(json) that takes and returns JSON strings.
//
//go:embed apply.js
var bundle string

// Request is one editor save call with the source it applies to.
type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
	Source string            `json:"source"`
}

// Response is what the browser receives, plus the new source when it changed.
type Response struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
	Source string          `json:"source,omitempty"`
}

// Config tunes one Engine.
type Config struct {
	Runtimes int           // interpreters kept ready; each runs one call at a time
	Deadline time.Duration // budget for one call, enforced by interrupting the interpreter
	MaxStack int           // call-stack limit, which ends pathological nesting early
}

var (
	// ErrDeadline means the source could not be parsed within the deadline.
	ErrDeadline = errors.New("edits: parsing the source exceeded the deadline")
	// ErrTooDeep means the source nests too deeply to parse.
	ErrTooDeep = errors.New("edits: the source nests too deeply to parse")
)

type runtime struct {
	vm    *goja.Runtime
	apply goja.Callable
}

// Engine applies editor requests on a pool of loaded interpreters.
type Engine struct {
	cfg  Config
	pool chan *runtime
}

// New compiles the bundle once and loads it into cfg.Runtimes interpreters.
func New(cfg Config) (*Engine, error) {
	if cfg.Runtimes <= 0 || cfg.Deadline <= 0 || cfg.MaxStack <= 0 {
		return nil, errors.New("edits: Runtimes, Deadline and MaxStack must be positive")
	}
	prog, err := goja.Compile("apply.js", bundle, false)
	if err != nil {
		return nil, fmt.Errorf("edits bundle: %w", err)
	}
	e := &Engine{cfg: cfg, pool: make(chan *runtime, cfg.Runtimes)}
	for range cfg.Runtimes {
		vm := goja.New()
		vm.SetMaxCallStackSize(cfg.MaxStack)
		// The upstream code ids comments with crypto.randomUUID.
		vm.Set("crypto", map[string]any{"randomUUID": uuid.NewString})
		if _, err := vm.RunProgram(prog); err != nil {
			return nil, fmt.Errorf("edits bundle: %w", err)
		}
		fn, ok := goja.AssertFunction(vm.Get("apply"))
		if !ok {
			return nil, errors.New("edits bundle: no apply function; run make edits")
		}
		e.pool <- &runtime{vm: vm, apply: fn}
	}
	return e, nil
}

// Apply runs one editor request. ctx bounds the wait for a free
// interpreter; the configured deadline bounds the call itself.
func (e *Engine) Apply(ctx context.Context, req Request) (Response, error) {
	var rt *runtime
	select {
	case rt = <-e.pool:
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	defer func() { e.pool <- rt }()
	in, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	timer := time.AfterFunc(e.cfg.Deadline, func() { rt.vm.Interrupt("deadline") })
	v, err := rt.apply(goja.Undefined(), rt.vm.ToValue(string(in)))
	timer.Stop()
	rt.vm.ClearInterrupt()
	// Neither can be caught by the JavaScript, so they are mapped here; the
	// interpreter is reusable after both.
	var interrupted *goja.InterruptedError
	var overflow *goja.StackOverflowError
	switch {
	case errors.As(err, &interrupted):
		return Response{}, ErrDeadline
	case errors.As(err, &overflow):
		return Response{}, ErrTooDeep
	case err != nil:
		return Response{}, fmt.Errorf("edits: %w", err)
	}
	var out Response
	if err := json.Unmarshal([]byte(v.String()), &out); err != nil {
		return Response{}, fmt.Errorf("edits reply: %w", err)
	}
	return out, nil
}
