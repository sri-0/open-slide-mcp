// Package validate compiles a slide module through the running Vite dev
// server, which catches syntax errors and unresolved imports without a
// separate toolchain. Type errors are not detected.
package validate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Result is the outcome of one compile check.
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

var (
	errRe = regexp.MustCompile(`(?s)const error = (\{.*?\})\n`)
	locRe = regexp.MustCompile(`:(\d+):(\d+) \]`)
)

// Check requests the transformed module for absPath from the dev server.
func Check(ctx context.Context, viteBase, absPath string) (Result, error) {
	url := fmt.Sprintf("%s/@fs%s?import&t=%d", viteBase, absPath, time.Now().UnixNano())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Sec-Fetch-Dest", "script")
	req.Header.Set("Accept", "*/*")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("dev server unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		return Result{OK: true}, nil
	}
	return ParseError(resp.StatusCode, string(body)), nil
}

// ParseError extracts the compiler message from Vite's HTML error page.
func ParseError(status int, body string) Result {
	r := Result{OK: false, Message: fmt.Sprintf("dev server returned %d", status)}
	m := errRe.FindStringSubmatch(body)
	if m == nil {
		return r
	}
	var e struct {
		Message string `json:"message"`
		Loc     *struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"loc"`
	}
	if err := json.Unmarshal([]byte(m[1]), &e); err != nil {
		return r
	}
	r.Message = strings.TrimSpace(e.Message)
	if e.Loc != nil {
		r.Line, r.Column = e.Loc.Line, e.Loc.Column
	} else if lm := locRe.FindStringSubmatch(e.Message); lm != nil {
		fmt.Sscanf(lm[1], "%d", &r.Line)
		fmt.Sscanf(lm[2], "%d", &r.Column)
	}
	return r
}
