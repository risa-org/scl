package handshake

import (
	"fmt"
	"time"

	"github.com/risa-org/scl/session"
)

// ResumeRequest is what the client sends when reconnecting.
type ResumeRequest struct {
	SessionID         string
	LastAckFromServer uint64
	ResumeToken       string
	RequestedAt       time.Time
}

// ResumeResult is what the handshake returns after processing a request.
type ResumeResult struct {
	Accepted    bool
	ResumePoint uint64
	Reason      string
}

// Rejection reason constants.
const (
	ReasonSessionNotFound    = "session_not_found"
	ReasonSessionExpired     = "session_expired"
	ReasonInvalidState       = "invalid_state"
	ReasonInvalidToken       = "invalid_token"
	ReasonInvalidResumePoint = "invalid_resume_point"
)

// SessionStore is the interface the handshake uses to look up sessions.
type SessionStore interface {
	Get(sessionID string) (*session.Session, *session.Sequencer, bool)
}

// Handler processes RESUME requests.
// It holds a reference to the store and the token issuer — stateless per request.
type Handler struct {
	store  SessionStore
	issuer *session.TokenIssuer
}

// NewHandler creates a handshake handler backed by the given store and token issuer.
// The issuer is used to verify resume tokens on every reconnect attempt.
func NewHandler(store SessionStore, issuer *session.TokenIssuer) *Handler {
	return &Handler{store: store, issuer: issuer}
}

// Resume processes a reconnect attempt.
//
// Steps:
//  1. Look up the session
//  2. Check it exists and isn't expired
//  3. Check it's in Disconnected state
//  4. Verify the resume token with HMAC
//  5. Compute resume point = min(client_last_ack, server_last_delivered)
//  6. Call ResumeTo on the sequencer
//  7. Transition session Disconnected → Resuming → Active
//  8. Return result
func (h *Handler) Resume(req ResumeRequest) ResumeResult {
	// step 1 — look up session
	sess, seq, ok := h.store.Get(req.SessionID)
	if !ok {
		return reject(ReasonSessionNotFound)
	}

	// step 2 — check expiry
	if sess.IsExpired() {
		return reject(ReasonSessionExpired)
	}

	// step 3 — must be Disconnected to resume
	if sess.State != session.StateDisconnected {
		return reject(ReasonInvalidState)
	}

	// step 4 — verify token using HMAC, constant-time comparison
	if err := h.issuer.Verify(sess.ID, req.ResumeToken); err != nil {
		return reject(ReasonInvalidToken)
	}

	// step 5 — compute resume point
	serverLastDelivered := seq.LastDelivered()
	resumePoint := min(req.LastAckFromServer, serverLastDelivered)

	// step 6 — restore sequencer to agreed resume point
	if err := seq.ResumeTo(resumePoint); err != nil {
		return reject(ReasonInvalidResumePoint)
	}

	// step 7 — transition Disconnected → Resuming → Active
	if ok := sess.Transition(session.StateResuming); !ok {
		return reject(ReasonInvalidState)
	}
	if ok := sess.Transition(session.StateActive); !ok {
		return reject(ReasonInvalidState)
	}

	// track reconnect for observability
	sess.ReconnectCount++

	return ResumeResult{
		Accepted:    true,
		ResumePoint: resumePoint,
	}
}

// Disconnect marks a session as disconnected.
// Called by the transport adapter when it detects a connection drop.
func (h *Handler) Disconnect(sessionID string) error {
	sess, _, ok := h.store.Get(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if ok := sess.Transition(session.StateDisconnected); !ok {
		return fmt.Errorf(
			"session %s cannot transition to disconnected from state %v",
			sessionID, sess.State,
		)
	}

	// if the store supports persistence, flush now.
	// disconnect is the moment sequencer durability matters most.
	if f, ok := h.store.(Flushable); ok {
		if err := f.Flush(); err != nil {
			return fmt.Errorf("failed to flush store on disconnect: %w", err)
		}
	}

	return nil
}

// reject builds a clean rejection result with a reason.
func reject(reason string) ResumeResult {
	return ResumeResult{
		Accepted: false,
		Reason:   reason,
	}
}

// min returns the smaller of two uint64 values.
func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// Flushable is optionally implemented by stores that persist state.
// Handler calls Flush() after marking a session disconnected so that
// sequencer positions are durable before the session goes dark.
type Flushable interface {
	Flush() error
}
