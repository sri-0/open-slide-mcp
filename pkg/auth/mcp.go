package auth

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Bearer gates the MCP endpoint. With a Verify func, every tools/call must
// carry a bearer token it accepts and an X-Root-Thread-ID header naming the
// caller's thread, which the tools then treat as the owner of every deck
// they touch. initialize, tools/list and the event stream pass through so a
// client can discover the server before it has a user. Without a Verify
// func the endpoint is open.
type Bearer struct {
	Verify func(ctx context.Context, token string) error
}

// Wrap returns next guarded as described on Bearer.
func (b Bearer) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.Verify == nil || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		// A batch is an array, which would hide a tools/call from the probe
		// below; no client of this server needs batches.
		if t := bytes.TrimSpace(body); len(t) == 0 || t[0] == '[' {
			http.Error(w, "one JSON-RPC request per POST", http.StatusBadRequest)
			return
		}
		var call struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &call); err != nil {
			http.Error(w, "body is not a JSON-RPC request", http.StatusBadRequest)
			return
		}
		if call.Method != "tools/call" {
			next.ServeHTTP(w, r)
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || b.Verify(r.Context(), strings.TrimSpace(tok)) != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="open-slide-mcp"`)
			http.Error(w, "a valid bearer token is required to call tools", http.StatusUnauthorized)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-Root-Thread-ID")) == "" {
			http.Error(w, "X-Root-Thread-ID must name the calling thread", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OIDC returns a Verify func for tokens issued by issuer for audience,
// checking signature, issuer, audience and expiry against the provider's
// published keys. discovery, when set, is where the provider's metadata is
// fetched; tokens must still carry issuer as iss.
func OIDC(ctx context.Context, issuer, discovery, audience string) (func(context.Context, string) error, error) {
	if discovery != "" && discovery != issuer {
		ctx = oidc.InsecureIssuerURLContext(ctx, issuer)
	}
	p, err := oidc.NewProvider(ctx, cmp.Or(discovery, issuer))
	if err != nil {
		return nil, fmt.Errorf("oidc provider: %w", err)
	}
	v := p.Verifier(&oidc.Config{ClientID: audience})
	return func(ctx context.Context, raw string) error {
		_, err := v.Verify(ctx, raw)
		return err
	}, nil
}
