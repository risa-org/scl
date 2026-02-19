package handshake

import (
	"testing"
	"time"

	"github.com/risa-org/scl/session"
)

// testIssuer returns a consistent TokenIssuer for tests.
func testIssuer() *session.TokenIssuer {
	return session.NewTokenIssuer([]byte("test-secret-key"))
}

// inMemoryStore is a simple in-memory SessionStore for testing.
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

// disconnectedSession creates a session already in Disconnected state, ready to resume.
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
	issuer := testIssuer()
	handler := NewHandler(store, issuer)

	sess := disconnectedSession(store, session.Interactive)

	result := handler.Resume(ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: 0,
		ResumeToken:       issuer.Issue(sess.ID),
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
	handler := NewHandler(store, testIssuer())

	result := handler.Resume(ResumeRequest{
		SessionID:   "nonexistent-id",
		ResumeToken: testIssuer().Issue("nonexistent-id"),
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
	issuer := testIssuer()
	handler := NewHandler(store, issuer)

	sess := disconnectedSession(store, session.Interactive)
	sess.CreatedAt = time.Now().Add(-10 * time.Minute)

	result := handler.Resume(ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: issuer.Issue(sess.ID),
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
	issuer := testIssuer()
	handler := NewHandler(store, issuer)

	sess, _ := session.NewSession(session.Interactive)
	seq := session.NewSequencer()
	sess.Transition(session.StateActive)
	store.Add(sess, seq)

	result := handler.Resume(ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: issuer.Issue(sess.ID),
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
	handler := NewHandler(store, testIssuer())

	sess := disconnectedSession(store, session.Interactive)

	result := handler.Resume(ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: "forged-token",
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
	issuer := testIssuer()
	handler := NewHandler(store, issuer)

	sess, _ := session.NewSession(session.Interactive)
	seq := session.NewSequencer()

	seq.Validate(1)
	seq.Validate(2)
	seq.Validate(10)

	sess.Transition(session.StateActive)
	sess.Transition(session.StateDisconnected)
	store.Add(sess, seq)

	result := handler.Resume(ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: 7,
		ResumeToken:       issuer.Issue(sess.ID),
		RequestedAt:       time.Now(),
	})

	if !result.Accepted {
		t.Errorf("expected resume accepted, got rejected: %s", result.Reason)
	}
	if result.ResumePoint != 7 {
		t.Errorf("expected resume point 7, got %d", result.ResumePoint)
	}
}

func TestDisconnect(t *testing.T) {
	store := newInMemoryStore()
	handler := NewHandler(store, testIssuer())

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
	handler := NewHandler(store, testIssuer())

	err := handler.Disconnect("ghost-session")
	if err == nil {
		t.Error("expected error when disconnecting unknown session")
	}
}
