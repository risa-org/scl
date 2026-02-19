package session

import "testing"

func TestIssueAndVerify(t *testing.T) {
	issuer, err := NewRandomTokenIssuer()
	if err != nil {
		t.Fatalf("failed to create issuer: %v", err)
	}

	token := issuer.Issue("session-abc")

	if err := issuer.Verify("session-abc", token); err != nil {
		t.Errorf("expected valid token to verify, got: %v", err)
	}
}

func TestVerifyWrongSessionID(t *testing.T) {
	issuer, _ := NewRandomTokenIssuer()

	// issue for one session, verify against another
	token := issuer.Issue("session-abc")

	err := issuer.Verify("session-xyz", token)
	if err == nil {
		t.Error("expected error verifying token against wrong session ID")
	}
}

func TestVerifyForgerdToken(t *testing.T) {
	issuer, _ := NewRandomTokenIssuer()

	// attacker knows the session ID but not the secret
	err := issuer.Verify("session-abc", "forged-token-value")
	if err == nil {
		t.Error("expected error for forged token")
	}
}

func TestTokensAreUniquePerSecret(t *testing.T) {
	// two issuers with different secrets produce different tokens
	// for the same session ID
	issuer1, _ := NewRandomTokenIssuer()
	issuer2, _ := NewRandomTokenIssuer()

	token1 := issuer1.Issue("session-abc")
	token2 := issuer2.Issue("session-abc")

	if token1 == token2 {
		t.Error("different secrets should produce different tokens for same session ID")
	}
}

func TestTokensAreDeterministic(t *testing.T) {
	// same secret + same session ID always produces same token
	// important for verification to work after process restarts
	// (when using a persisted secret)
	secret := []byte("fixed-secret-for-testing")
	issuer1 := NewTokenIssuer(secret)
	issuer2 := NewTokenIssuer(secret)

	token1 := issuer1.Issue("session-abc")
	token2 := issuer2.Issue("session-abc")

	if token1 != token2 {
		t.Error("same secret and session ID should always produce same token")
	}
}

func TestVerifyWrongTokenReturnsCorrectError(t *testing.T) {
	issuer, _ := NewRandomTokenIssuer()

	err := issuer.Verify("session-abc", "bad-token")
	if err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}
