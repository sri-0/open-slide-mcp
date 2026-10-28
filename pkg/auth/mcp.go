package auth

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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
		// The same decoder the handler uses: a batch (an array) or anything
		// that is not one request fails here instead of slipping past.
		msg, err := jsonrpc.DecodeMessage(body)
		call, ok := msg.(*jsonrpc.Request)
		if err != nil || !ok {
			http.Error(w, "body must be one JSON-RPC request", http.StatusBadRequest)
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
