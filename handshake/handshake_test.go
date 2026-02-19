package handshake

import (
	"testing"
	"time"

	"github.com/risa-org/scl/session"
)

// inMemoryStore is a simple in-memory SessionStore for testing.
// This is exactly the kind of fake the design doc calls for in testing support.
type inMemoryStore struct {
	sessions   map[string]*session.Session
	sequencers map[string]*session.Sequencer
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{
		sessions:   make(map[string]*session.Session),
		sequencers: make(map[string]*session.Sequencer),
	}
}

func (s *inMemoryStore) Add(sess *session.Session, seq *session.Sequencer) {
	s.sessions[sess.ID] = sess
	s.sequencers[sess.ID] = seq
}

func (s *inMemoryStore) Get(sessionID string) (*session.Session, *session.Sequencer, bool) {
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil, false
	}
	return sess, s.sequencers[sessionID], true
}

// helper: creates a session already in Disconnected state, ready to resume
func disconnectedSession(store *inMemoryStore, policy session.Policy) *session.Session {
	sess, _ := session.NewSession(policy)
	seq := session.NewSequencer()
	sess.Transition(session.StateActive)
	sess.Transition(session.StateDisconnected)
	store.Add(sess, seq)
	return sess
}

// --- Tests ---

func TestResumeSuccess(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	sess := disconnectedSession(store, session.Interactive)

	result := handler.Resume(ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: 0,
		ResumeToken:       sess.ID, // token = ID for now
		RequestedAt:       time.Now(),
	})

	if !result.Accepted {
		t.Errorf("expected resume to be accepted, got rejected: %s", result.Reason)
	}
	if sess.State != session.StateActive {
		t.Errorf("expected session to be Active after resume, got %v", sess.State)
	}
	if sess.ReconnectCount != 1 {
		t.Errorf("expected ReconnectCount 1, got %d", sess.ReconnectCount)
	}
}

func TestResumeSessionNotFound(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	result := handler.Resume(ResumeRequest{
		SessionID:   "nonexistent-id",
		ResumeToken: "nonexistent-id",
		RequestedAt: time.Now(),
	})

	if result.Accepted {
		t.Error("expected rejection for unknown session ID")
	}
	if result.Reason != ReasonSessionNotFound {
		t.Errorf("expected reason %s, got %s", ReasonSessionNotFound, result.Reason)
	}
}

func TestResumeExpiredSession(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	sess := disconnectedSession(store, session.Interactive)
	// backdate to simulate TTL exceeded
	sess.CreatedAt = time.Now().Add(-10 * time.Minute)

	result := handler.Resume(ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: sess.ID,
		RequestedAt: time.Now(),
	})

	if result.Accepted {
		t.Error("expected rejection for expired session")
	}
	if result.Reason != ReasonSessionExpired {
		t.Errorf("expected reason %s, got %s", ReasonSessionExpired, result.Reason)
	}
}

func TestResumeWrongState(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	// session is Active, not Disconnected — resume should be rejected
	sess, _ := session.NewSession(session.Interactive)
	seq := session.NewSequencer()
	sess.Transition(session.StateActive)
	store.Add(sess, seq)

	result := handler.Resume(ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: sess.ID,
		RequestedAt: time.Now(),
	})

	if result.Accepted {
		t.Error("expected rejection for session not in Disconnected state")
	}
	if result.Reason != ReasonInvalidState {
		t.Errorf("expected reason %s, got %s", ReasonInvalidState, result.Reason)
	}
}

func TestResumeInvalidToken(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	sess := disconnectedSession(store, session.Interactive)

	result := handler.Resume(ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: "wrong-token",
		RequestedAt: time.Now(),
	})

	if result.Accepted {
		t.Error("expected rejection for invalid token")
	}
	if result.Reason != ReasonInvalidToken {
		t.Errorf("expected reason %s, got %s", ReasonInvalidToken, result.Reason)
	}
}

func TestResumePointIsMinOfClientAndServer(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	sess, _ := session.NewSession(session.Interactive)
	seq := session.NewSequencer()

	// simulate server having delivered up to seq 10
	seq.Validate(1)
	seq.Validate(2)
	seq.Validate(10)

	sess.Transition(session.StateActive)
	sess.Transition(session.StateDisconnected)
	store.Add(sess, seq)

	// client only got up to seq 7
	result := handler.Resume(ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: 7,
		ResumeToken:       sess.ID,
		RequestedAt:       time.Now(),
	})

	if !result.Accepted {
		t.Errorf("expected resume accepted, got rejected: %s", result.Reason)
	}
	// resume point should be 7 — the min of 7 and 10
	if result.ResumePoint != 7 {
		t.Errorf("expected resume point 7, got %d", result.ResumePoint)
	}
}

func TestDisconnect(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	sess, _ := session.NewSession(session.Interactive)
	seq := session.NewSequencer()
	sess.Transition(session.StateActive)
	store.Add(sess, seq)

	err := handler.Disconnect(sess.ID)
	if err != nil {
		t.Errorf("expected no error on disconnect, got: %v", err)
	}
	if sess.State != session.StateDisconnected {
		t.Errorf("expected Disconnected state, got %v", sess.State)
	}
}

func TestDisconnectUnknownSession(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store)

	err := handler.Disconnect("ghost-session")
	if err == nil {
		t.Error("expected error when disconnecting unknown session")
	}
}
