// Package auth mints and verifies the signed tokens that gate every request
// to a deck origin. A token names exactly one deck and one use: a link token
// rides on the preview URL and is short-lived because the embedding app
// stores that URL; the browser exchanges it for a session token in a
// host-only cookie, which lives longer so a stored link keeps opening the
// deck. A link is a capability: it can be exchanged again until it expires. Every request on a deck host is checked against the deck the host
// serves, so a token for deck A is worthless on deck B's origin, and a link
// token is never accepted as a cookie or the other way round.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	// ErrInvalidToken is returned for a token that is malformed, signed with
	// another key or method, expired, or missing its deck.
	ErrInvalidToken = errors.New("invalid or expired deck token; request a new preview_url")
)

// Use says what a token is for; it is a claim, so the two cannot be swapped.
type Use string

const (
	UseLink    Use = "link"    // carried on a preview URL, exchanged once for a session
	UseSession Use = "session" // held in the deck origin's cookie
)

type claims struct {
	jwt.RegisteredClaims
	Use Use `json:"use"`
}

// Signer issues and checks deck tokens with one HMAC-SHA256 key.
type Signer struct {
	secret []byte
	now    func() time.Time
}

// New returns a Signer for the given key.
func New(secret []byte) (*Signer, error) {
	if len(secret) < 32 {
		return nil, errors.New("PREVIEW_SECRET must be at least 32 bytes")
	}
	return &Signer{secret: secret, now: time.Now}, nil
}

// Mint returns a signed token for deckID with the given use and lifetime,
// and the time it expires.
func (s *Signer) Mint(deckID string, use Use, ttl time.Duration) (string, time.Time, error) {
	var jti [16]byte
	if _, err := rand.Read(jti[:]); err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	exp := now.Add(ttl)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   deckID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        hex.EncodeToString(jti[:]),
		},
		Use: use,
	}).SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("deck token: %w", err)
	}
	return tok, exp, nil
}

// Verify checks a token of the given use and returns the deck it grants
// access to.
func (s *Signer) Verify(token string, use Use) (string, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil || c.Subject == "" || c.Use != use {
		return "", ErrInvalidToken
	}
	return c.Subject, nil
}
