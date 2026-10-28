package compile

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"

	"github.com/evanw/esbuild/pkg/api"
)

// Child is the child side of Compile, run by the "compile" subcommand of
// this binary: it reads one JSON request from stdin, applies the resource
// limits, runs esbuild and writes one JSON response to stdout. Build
// problems are reported inside the response; the error is for a request
// that cannot be read, a scratch directory that cannot be made, or a
// response that cannot be written.
func Child(stdin io.Reader, stdout io.Writer) error {
	var req request
	if err := json.NewDecoder(stdin).Decode(&req); err != nil {
		return fmt.Errorf("compile request: %w", err)
	}
	// Both limits are best effort: darwin accepts RLIMIT_CPU but ignores
	// RLIMIT_AS, and a limit that cannot be set must not stop a compile
	// that the deadline in the parent still bounds.
	if m := req.Config.MaxMemory; m > 0 {
		syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: uint64(m), Max: uint64(m)})
	}
	cpu := uint64(math.Ceil(req.Config.Timeout.Seconds()))
	syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: cpu, Max: cpu})
	// esbuild recurses on nesting depth, so a pathological deck dies by stack
	// overflow; a quarter of Go's default limit ends it sooner and smaller.
	debug.SetMaxStack(256 << 20)
	resp, err := build(req.Config, req.Source)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(resp)
}

// build runs esbuild on src as index.tsx inside an empty scratch directory,
// so relative imports resolve nowhere, and gates imports twice: the
// resolve plugin refuses anything outside the allow lists as it is seen,
// and the metafile proves afterwards that every input was the entry or a
// file under ModulesDir.
func build(cfg Config, src string) (response, error) {
	dir, err := os.MkdirTemp("", "deck")
	if err != nil {
		return response{}, fmt.Errorf("compile scratch dir: %w", err)
	}
	defer os.RemoveAll(dir)
	// esbuild reports real paths, so compare against real paths.
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return response{}, fmt.Errorf("compile scratch dir: %w", err)
	}
	modules := cfg.ModulesDir
	if modules != "" {
		if modules, err = filepath.EvalSymlinks(modules); err != nil {
			return response{}, fmt.Errorf("compile modules dir: %w", err)
		}
		modules += string(filepath.Separator)
	}
	entry := filepath.Join(dir, "index.tsx")
	allowed := strings.Join(slices.Concat(cfg.External, cfg.Bundled), ", ")
	// An external with a known export list is reached through a shim module
	// that re-exports exactly those names, so esbuild checks the deck's
	// named imports against it; the shim's own import stays external.
	const shim = "shim"
	gate := api.Plugin{Name: "allow", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			bare := !strings.HasPrefix(a.Path, ".") && !filepath.IsAbs(a.Path)
			switch {
			case a.Namespace == shim:
				return api.OnResolveResult{Path: a.Path, External: true}, nil
			case a.Importer == entry && bare && slices.Contains(cfg.External, a.Path) && cfg.Exports[a.Path] != nil:
				return api.OnResolveResult{Path: a.Path, Namespace: shim}, nil
			case a.Importer == entry && bare && slices.Contains(cfg.External, a.Path):
				return api.OnResolveResult{Path: a.Path, External: true}, nil
			case a.Importer == entry && bare && slices.Contains(cfg.Bundled, a.Path):
				return api.OnResolveResult{}, nil
			case a.Importer != entry && modules != "" && strings.HasPrefix(a.Importer, modules):
				// A bundled package's own imports; the External option
				// still keeps its imports of react external.
				return api.OnResolveResult{}, nil
			}
			msg := fmt.Sprintf("import not allowed: %s (allowed: %s)", a.Path, allowed)
			return api.OnResolveResult{Errors: []api.Message{{Text: msg}}}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: shim}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			var sb strings.Builder
			for _, n := range cfg.Exports[a.Path] {
				fmt.Fprintf(&sb, "export { %s } from %q;\n", n, a.Path)
			}
			contents := sb.String()
			return api.OnLoadResult{Contents: &contents, Loader: api.LoaderJS, ResolveDir: dir}, nil
		})
	}}
	opts := api.BuildOptions{
		Stdin:         &api.StdinOptions{Contents: src, Sourcefile: "index.tsx", Loader: api.LoaderTSX, ResolveDir: dir},
		AbsWorkingDir: dir,
		Outdir:        "out",
		Bundle:        true,
		Format:        api.FormatESModule,
		Platform:      api.PlatformBrowser,
		Engines: []api.Engine{
			{Name: api.EngineChrome, Version: "111"},
			{Name: api.EngineEdge, Version: "111"},
			{Name: api.EngineFirefox, Version: "114"},
			{Name: api.EngineSafari, Version: "16.4"},
		},
		JSX:             api.JSXAutomatic,
		JSXImportSource: "react",
		JSXDev:          true,
		External:        cfg.External,
		Define:          map[string]string{"process.env.NODE_ENV": `"production"`},
		Sourcemap:       api.SourceMapExternal,
		TreeShaking:     api.TreeShakingTrue,
		Metafile:        true,
		Write:           false,
		LogLimit:        20,
		LogLevel:        api.LogLevelSilent,
		Plugins:         []api.Plugin{gate},
	}
	if cfg.ModulesDir != "" {
		opts.NodePaths = []string{cfg.ModulesDir}
	}
	r := api.Build(opts)
	if len(r.Errors) > 0 {
		e := r.Errors[0]
		resp := response{Result: Result{Message: strings.ReplaceAll(e.Text, shim+":", "")}}
		if e.Location != nil {
			resp.Line, resp.Column = e.Location.Line, e.Location.Column
		}
		return resp, nil
	}
	var meta struct {
		Inputs map[string]struct{} `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(r.Metafile), &meta); err != nil {
		return response{}, fmt.Errorf("compile metafile: %w", err)
	}
	for in := range meta.Inputs {
		if strings.HasPrefix(in, shim+":") {
			continue
		}
		if abs := filepath.Join(dir, in); abs != entry && (modules == "" || !strings.HasPrefix(abs, modules)) {
			return response{Result: Result{Message: "input not allowed: " + in}}, nil
		}
	}
	resp := response{Result: Result{OK: true}}
	for _, f := range r.OutputFiles {
		switch filepath.Ext(f.Path) {
		case ".js":
			resp.JS = f.Contents
		case ".map":
			resp.Map = f.Contents
		}
	}
	return resp, nil
}
