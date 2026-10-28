package shell

import (
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// Origins maps a deck id to the URL that serves it and back, from the
// DECK_ORIGIN pattern. With {deck} as the first host label every deck has
// its own origin (https://{deck}.decks.example.com) and the browser keeps
// decks apart. With {deck} as the path (https://decks.example.com/{deck})
// decks share one origin and are kept apart only by their sessions; the
// README says what that gives up.
type Origins struct {
	scheme string
	host   string // path mode: the shared host, lowercase; empty in host mode
	suffix string // host mode: everything after the deck label, lowercase
}

// ParseOrigins checks the pattern once at startup. The session cookie is
// Secure, so http is allowed only where browsers treat it as secure:
// localhost and its subdomains.
func ParseOrigins(pattern string) (Origins, error) {
	// url.Parse rejects braces in a host, so the label is stood in for.
	u, err := url.Parse(strings.Replace(pattern, "{deck}", "deck", 1))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Origins{}, errors.New("DECK_ORIGIN must be an http or https URL")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && !strings.HasSuffix(u.Hostname(), ".localhost") {
		return Origins{}, errors.New("DECK_ORIGIN must be https unless the host is localhost or ends in .localhost")
	}
	switch {
	case strings.Contains(pattern, "://{deck}.") && u.Path == "":
		return Origins{scheme: u.Scheme, suffix: strings.ToLower(strings.TrimPrefix(u.Host, "deck"))}, nil
	case strings.HasSuffix(pattern, "/{deck}") && u.Path == "/deck":
		return Origins{scheme: u.Scheme, host: strings.ToLower(u.Host)}, nil
	}
	return Origins{}, errors.New("DECK_ORIGIN must be scheme://{deck}.<domain>[:port] (one origin per deck) or scheme://<host>[:port]/{deck} (one shared origin)")
}

// PathMode reports whether decks share one origin.
func (o Origins) PathMode() bool { return o.host != "" }

// Origin returns the origin a deck's page runs on.
func (o Origins) Origin(deck string) string {
	if o.PathMode() {
		return o.scheme + "://" + o.host
	}
	return o.scheme + "://" + deck + o.suffix
}

// URL returns the deck's base URL without a trailing slash.
func (o Origins) URL(deck string) string {
	if o.PathMode() {
		return o.Origin(deck) + "/" + deck
	}
	return o.Origin(deck)
}

// Base returns the path the deck's page, cookie and routes live under.
func (o Origins) Base(deck string) string {
	if o.PathMode() {
		return "/" + deck + "/"
	}
	return "/"
}

// Deck returns the deck a request addresses and the request path with the
// deck's base removed, or false when the request is not for a deck.
func (o Origins) Deck(host, path string) (string, string, bool) {
	host = strings.ToLower(host)
	if o.PathMode() {
		if host != o.host {
			return "", "", false
		}
		deck, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if uuid.Validate(deck) != nil {
			return "", "", false
		}
		return deck, "/" + rest, true
	}
	if !strings.HasSuffix(host, o.suffix) {
		return "", "", false
	}
	deck := strings.TrimSuffix(host, o.suffix)
	if uuid.Validate(deck) != nil {
		return "", "", false
	}
	return deck, path, true
}
