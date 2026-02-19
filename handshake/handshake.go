package handshake

import (
	"fmt"
	"time"

	"github.com/risa-org/scl/session"
)

// ResumeRequest is what the client sends when reconnecting.
// It carries everything the server needs to decide whether to restore the session.
type ResumeRequest struct {
	SessionID         string // which session the client wants to resume
	LastAckFromServer uint64 // highest seq the client actually received from server
	ResumeToken       string // opaque token issued at session creation, proves ownership
	RequestedAt       time.Time
}

// ResumeResult is what the handshake returns after processing a request.
// Either the session is restored and Active, or it's rejected with a reason.
type ResumeResult struct {
	Accepted    bool
	ResumePoint uint64 // agreed seq to resume from — min(client_ack, server_delivered)
	Reason      string // populated on rejection, empty on success
}

// RejectionReason constants — clear, named reasons for rejection.
// This feeds directly into observability.
const (
	ReasonSessionNotFound    = "session_not_found"
	ReasonSessionExpired     = "session_expired"
	ReasonInvalidState       = "invalid_state"
	ReasonInvalidToken       = "invalid_token"
	ReasonInvalidResumePoint = "invalid_resume_point"
)

// SessionStore is the interface the handshake uses to look up sessions.
// We define it as an interface here so the handshake package doesn't need
// to know anything about how sessions are stored — memory, Redis, whatever.
// This is called dependency inversion — depend on behavior, not implementation.
type SessionStore interface {
	Get(sessionID string) (*session.Session, *session.Sequencer, bool)
}

// Handler processes RESUME requests.
// It holds a reference to the store but nothing else — stateless per request.
type Handler struct {
	store SessionStore
}

// NewHandler creates a handshake handler backed by the given store.
func NewHandler(store SessionStore) *Handler {
	return &Handler{store: store}
}

// Resume processes a reconnect attempt.
// This is the core of the RESUME protocol from the design doc.
//
// Steps:
//  1. Look up the session
//  2. Check it exists and isn't expired
//  3. Check it's in a resumable state (must be Disconnected)
//  4. Validate the resume token
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

	// step 3 — must be in Disconnected state to resume
	// Active means it never disconnected (duplicate resume attempt)
	// Expired/Connecting are invalid
	if sess.State != session.StateDisconnected {
		return reject(ReasonInvalidState)
	}

	// step 4 — validate token
	// For now this is a simple equality check.
	// Later this becomes a cryptographic verification.
	if req.ResumeToken != sess.ID {
		return reject(ReasonInvalidToken)
	}

	// step 5 — compute resume point
	// Server's lastDelivered is authoritative.
	// We take the minimum — the point both sides are certain about.
	serverLastDelivered := seq.LastDelivered()
	resumePoint := min(req.LastAckFromServer, serverLastDelivered)

	// step 6 — restore sequencer to agreed resume point
	if err := seq.ResumeTo(resumePoint); err != nil {
		return reject(ReasonInvalidResumePoint)
	}

	// step 7 — transition session state
	// Disconnected → Resuming → Active
	// Two transitions because Resuming is a distinct observable state.
	// Something could go wrong between these — auth hook, resource check, etc.
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

// reject is a helper to build a clean rejection result with a reason.
func reject(reason string) ResumeResult {
	return ResumeResult{
		Accepted: false,
		Reason:   reason,
	}
}

// min returns the smaller of two uint64 values.
// Go 1.21+ has a built-in min, but we define it explicitly for clarity
// and compatibility with earlier versions.
func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// Disconnect marks a session as disconnected.
// Called by the transport adapter when it detects a connection drop.
// Returns an error if the session can't be found or isn't in a valid state.
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

	return nil
}
