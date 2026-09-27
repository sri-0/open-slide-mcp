package proxy

import (
	"testing"
	"time"
)

func TestTokenRoundTrip(t *testing.T) {
	p, err := New("http://127.0.0.1:5173", []byte("secret"), true, time.Hour, "http://localhost:8090", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := p.PreviewURL("t-abc-deck")
	tok := u[len(u)-len(u)+len("http://localhost:8090/preview/t-abc-deck?t="):]
	deck, ok := p.Verify(tok)
	if !ok || deck != "t-abc-deck" {
		t.Fatalf("verify failed: %q %v", deck, ok)
	}
	if _, ok := p.Verify(tok + "x"); ok {
		t.Fatal("tampered token accepted")
	}
	p2, _ := New("http://127.0.0.1:5173", []byte("other"), true, time.Hour, "http://localhost:8090", nil)
	if _, ok := p2.Verify(tok); ok {
		t.Fatal("token accepted with wrong secret")
	}
	p3, _ := New("http://127.0.0.1:5173", []byte("secret"), true, -time.Hour, "http://localhost:8090", nil)
	u3, _ := p3.PreviewURL("d")
	if _, ok := p3.Verify(u3[len("http://localhost:8090/preview/d?t="):]); ok {
		t.Fatal("expired token accepted")
	}
}
