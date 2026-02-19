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

// RetransmitMessage carries a message to be resent after resume.
type RetransmitMessage struct {
	Seq     uint64
	Payload []byte
}

// ResumeResult is what the handshake returns after processing a request.
type ResumeResult struct {
	Accepted          bool
	ResumePoint       uint64
	Reason            string
	Retransmit        []RetransmitMessage // messages client missed, in order — may be empty
	Partial           bool                // true if buffer rolled over and some messages are unrecoverable
	OldestRecoverable uint64              // if Partial, the oldest seq we can recover from
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

// Flushable is optionally implemented by stores that persist state.
type Flushable interface {
	Flush() error
}

// Handler processes RESUME requests.
type Handler struct {
	store  SessionStore
	issuer *session.TokenIssuer
}

// NewHandler creates a handshake handler backed by the given store and token issuer.
func NewHandler(store SessionStore, issuer *session.TokenIssuer) *Handler {
	return &Handler{store: store, issuer: issuer}
}

// Resume processes a reconnect attempt.
func (h *Handler) Resume(req ResumeRequest) ResumeResult {
	sess, seq, ok := h.store.Get(req.SessionID)
	if !ok {
		return reject(ReasonSessionNotFound)
	}

	if sess.IsExpired() {
		return reject(ReasonSessionExpired)
	}

	if sess.State != session.StateDisconnected {
		return reject(ReasonInvalidState)
	}

	if err := h.issuer.Verify(sess.ID, req.ResumeToken); err != nil {
		return reject(ReasonInvalidToken)
	}

	serverLastDelivered := seq.LastDelivered()
	resumePoint := min(req.LastAckFromServer, serverLastDelivered)

	if err := seq.ResumeTo(resumePoint); err != nil {
		return reject(ReasonInvalidResumePoint)
	}

	if ok := sess.Transition(session.StateResuming); !ok {
		return reject(ReasonInvalidState)
	}
	if ok := sess.Transition(session.StateActive); !ok {
		return reject(ReasonInvalidState)
	}

	sess.ReconnectCount++

	sentMsgs, fullRecovery := seq.Retransmit(resumePoint)

	result := ResumeResult{
		Accepted:    true,
		ResumePoint: resumePoint,
		Partial:     !fullRecovery,
	}

	if !fullRecovery {
		result.OldestRecoverable = seq.OldestRecoverable()
	}

	for _, m := range sentMsgs {
		result.Retransmit = append(result.Retransmit, RetransmitMessage{
			Seq:     m.Seq,
			Payload: m.Payload,
		})
	}

	return result
}

// Disconnect marks a session as disconnected.
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

	if f, ok := h.store.(Flushable); ok {
		if err := f.Flush(); err != nil {
			return fmt.Errorf("failed to flush store on disconnect: %w", err)
		}
	}

	return nil
}

func reject(reason string) ResumeResult {
	return ResumeResult{
		Accepted: false,
		Reason:   reason,
	}
}

func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
