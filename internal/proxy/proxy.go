// Package proxy fronts the open-slide dev server. It mints deck-scoped
// preview tokens, exchanges them for a session cookie, and blocks the
// dev-server endpoints that must never be reachable by a browser.
package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

const cookieName = "osmcp_session"

// Blocked dev-server endpoints. These mutate the workspace outside the
// per-deck editing flow or restart the process.
var blocked = map[string]bool{
	"/__restart-server": true,
	"/__slides":         true,
	"/__folders":        true,
}

// Proxy is the echo handler set for the dev server.
type Proxy struct {
	Target    *url.URL
	Secret    []byte
	Auth      bool
	TokenTTL  time.Duration
	PublicURL string
	Secure    bool // mark the cookie Secure; required for SameSite=None
	OnTouch   func(deckID string)
	rp        *httputil.ReverseProxy
}

// New builds a proxy for the given dev server base URL.
func New(viteBase string, secret []byte, auth bool, ttl time.Duration, publicURL string, onTouch func(string)) (*Proxy, error) {
	target, err := url.Parse(viteBase)
	if err != nil {
		return nil, err
	}
	p := &Proxy{Target: target, Secret: secret, Auth: auth, TokenTTL: ttl, PublicURL: publicURL, OnTouch: onTouch}
	p.Secure = strings.HasPrefix(publicURL, "https://") || strings.Contains(publicURL, "localhost")
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
			r.SetXForwarded()
		},
		FlushInterval: -1,
	}
	return p, nil
}

// Register mounts the preview entry point and the catch-all proxy.
func (p *Proxy) Register(e *echo.Echo) {
	e.GET("/preview/:deck", p.preview)
	e.Any("/*", p.forward)
}

// PreviewURL returns a signed link that opens deckID in the proxied UI.
func (p *Proxy) PreviewURL(deckID string) (string, time.Time) {
	exp := time.Now().Add(p.TokenTTL)
	u := p.PublicURL + "/preview/" + url.PathEscape(deckID)
	if p.Auth {
		u += "?t=" + p.sign(deckID, exp)
	}
	return u, exp
}

func (p *Proxy) sign(deckID string, exp time.Time) string {
	payload := deckID + "|" + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, p.Secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a token and returns the deck it grants access to.
func (p *Proxy) Verify(token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, p.Secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", false
	}
	fields := strings.SplitN(string(payload), "|", 2)
	if len(fields) != 2 {
		return "", false
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return fields[0], true
}

func (p *Proxy) preview(c echo.Context) error {
	deck := c.Param("deck")
	if p.Auth {
		granted, ok := p.Verify(c.QueryParam("t"))
		if !ok || granted != deck {
			return c.String(http.StatusForbidden, "invalid or expired preview token")
		}
		c.SetCookie(&http.Cookie{
			Name:     cookieName,
			Value:    c.QueryParam("t"),
			Path:     "/",
			HttpOnly: true,
			Secure:   p.Secure,
			SameSite: http.SameSiteNoneMode,
			Expires:  time.Now().Add(p.TokenTTL),
		})
	}
	if p.OnTouch != nil {
		p.OnTouch(deck)
	}
	return c.Redirect(http.StatusFound, "/s/"+url.PathEscape(deck))
}

func (p *Proxy) forward(c echo.Context) error {
	path := c.Request().URL.Path
	if blocked[path] || strings.HasPrefix(path, "/__restart-server") {
		return c.String(http.StatusForbidden, "endpoint disabled by open-slide-mcp")
	}
	if p.Auth {
		ck, err := c.Cookie(cookieName)
		if err != nil {
			return c.String(http.StatusUnauthorized, "open the deck through its preview_url first")
		}
		deck, ok := p.Verify(ck.Value)
		if !ok {
			return c.String(http.StatusUnauthorized, "preview session expired; request a new preview_url")
		}
		if strings.HasPrefix(path, "/s/") {
			if seg := strings.SplitN(strings.TrimPrefix(path, "/s/"), "/", 2)[0]; seg != deck {
				return c.String(http.StatusForbidden, fmt.Sprintf("this session only grants access to deck %s", deck))
			}
		}
	}
	p.rp.ServeHTTP(c.Response(), c.Request())
	return nil
}
