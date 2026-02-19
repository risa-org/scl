package session

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
)

var ErrInvalidToken = errors.New("invalid resume token")

// TokenIssuer generates and verifies HMAC-signed resume tokens.
// The secret key never leaves the server — clients only ever see the token.
type TokenIssuer struct {
	secret []byte
}

// NewTokenIssuer creates an issuer with the given secret key.
// The secret should be at least 32 bytes of random data.
// In production this comes from an environment variable or secrets manager,
// never hardcoded.
func NewTokenIssuer(secret []byte) *TokenIssuer {
	return &TokenIssuer{secret: secret}
}

// NewRandomTokenIssuer generates a fresh random secret key.
// Useful for single-process servers where the secret doesn't need
// to survive restarts. If the process restarts, all sessions are
// invalidated — which is acceptable for ephemeral and interactive policies.
func NewRandomTokenIssuer() (*TokenIssuer, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &TokenIssuer{secret: secret}, nil
}

// Issue generates a token for the given session ID.
// Token = hex(HMAC-SHA256(secret, sessionID))
// The session ID is the message — the secret key signs it.
func (t *TokenIssuer) Issue(sessionID string) string {
	mac := hmac.New(sha256.New, t.secret)
	mac.Write([]byte(sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks whether the given token is valid for the session ID.
// Uses constant-time comparison to prevent timing attacks.
// Returns nil if valid, ErrInvalidToken if not.
func (t *TokenIssuer) Verify(sessionID, token string) error {
	expected := t.Issue(sessionID)

	// constant-time comparison — does not short-circuit on mismatch.
	// Regular == leaks information about where the strings differ
	// through response timing. subtle.ConstantTimeCompare eliminates that.
	if subtle.ConstantTimeCompare([]byte(expected), []byte(token)) != 1 {
		return ErrInvalidToken
	}

	return nil
}
