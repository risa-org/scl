package integration

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/risa-org/scl/handshake"
	"github.com/risa-org/scl/session"
	"github.com/risa-org/scl/transport"
	tcpadapter "github.com/risa-org/scl/transport/tcp"
)

// issuer is shared across all integration tests.
var issuer = session.NewTokenIssuer([]byte("integration-test-secret"))

// ------------------------------------------------------------
// SessionManager
// ------------------------------------------------------------

type entry struct {
	sess *session.Session
	seq  *session.Sequencer
}

type SessionManager struct {
	mu      sync.RWMutex
	entries map[string]*entry
}

func newSessionManager() *SessionManager {
	return &SessionManager{entries: make(map[string]*entry)}
}

func (m *SessionManager) Create(policy session.Policy) (*session.Session, *session.Sequencer, error) {
	sess, err := session.NewSession(policy)
	if err != nil {
		return nil, nil, err
	}
	seq := session.NewSequencer()
	m.mu.Lock()
	m.entries[sess.ID] = &entry{sess: sess, seq: seq}
	m.mu.Unlock()
	return sess, seq, nil
}

func (m *SessionManager) Get(sessionID string) (*session.Session, *session.Sequencer, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[sessionID]
	if !ok {
		return nil, nil, false
	}
	return e.sess, e.seq, true
}

// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------

func connPair(t *testing.T) (*tcpadapter.Adapter, *tcpadapter.Adapter) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	return tcpadapter.New(serverConn), tcpadapter.New(clientConn)
}

// ------------------------------------------------------------
// Tests
// ------------------------------------------------------------

func TestFullSessionLifecycle(t *testing.T) {
	manager := newSessionManager()
	handler := handshake.NewHandler(manager, issuer)

	sess, seq, err := manager.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)

	server, client := connPair(t)
	defer server.Close()
	defer client.Close()

	for i := uint64(1); i <= 3; i++ {
		err := client.Send(transport.Message{
			Seq:     seq.Next(),
			Payload: []byte("hello"),
		})
		if err != nil {
			t.Fatalf("Send %d failed: %v", i, err)
		}
	}

	for i := uint64(1); i <= 3; i++ {
		select {
		case msg := <-server.Receive():
			verdict := seq.Validate(msg.Seq)
			if verdict != session.Deliver {
				t.Errorf("message %d should be delivered, got verdict %v", i, verdict)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message %d", i)
		}
	}

	err = handler.Disconnect(sess.ID)
	if err != nil {
		t.Fatalf("disconnect failed: %v", err)
	}
	if sess.State != session.StateDisconnected {
		t.Errorf("expected Disconnected, got %v", sess.State)
	}
}

func TestResumeAfterDisconnect(t *testing.T) {
	manager := newSessionManager()
	handler := handshake.NewHandler(manager, issuer)

	sess, seq, err := manager.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)

	// issue token at session creation — client stores this
	token := issuer.Issue(sess.ID)

	server, client := connPair(t)

	for i := 0; i < 5; i++ {
		client.Send(transport.Message{
			Seq:     seq.Next(),
			Payload: []byte("pre-disconnect"),
		})
	}

	for i := 0; i < 5; i++ {
		select {
		case msg := <-server.Receive():
			seq.Validate(msg.Seq)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out receiving message %d", i+1)
		}
	}

	lastDeliveredBeforeDisconnect := seq.LastDelivered()

	client.Close()
	server.Close()
	handler.Disconnect(sess.ID)

	server2, client2 := connPair(t)
	defer server2.Close()
	defer client2.Close()

	result := handler.Resume(handshake.ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: lastDeliveredBeforeDisconnect,
		ResumeToken:       token,
		RequestedAt:       time.Now(),
	})

	if !result.Accepted {
		t.Fatalf("RESUME rejected: %s", result.Reason)
	}
	if sess.State != session.StateActive {
		t.Errorf("expected Active after resume, got %v", sess.State)
	}
	if sess.ReconnectCount != 1 {
		t.Errorf("expected ReconnectCount 1, got %d", sess.ReconnectCount)
	}

	for i := 0; i < 3; i++ {
		err := client2.Send(transport.Message{
			Seq:     seq.Next(),
			Payload: []byte("post-resume"),
		})
		if err != nil {
			t.Fatalf("post-resume Send %d failed: %v", i+1, err)
		}
	}

	for i := 0; i < 3; i++ {
		select {
		case msg := <-server2.Receive():
			verdict := seq.Validate(msg.Seq)
			if verdict != session.Deliver {
				t.Errorf("post-resume message %d should deliver, got %v", i+1, verdict)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out on post-resume message %d", i+1)
		}
	}

	if seq.LastDelivered() != 8 {
		t.Errorf("expected lastDelivered 8, got %d", seq.LastDelivered())
	}
}

func TestForgedTokenRejected(t *testing.T) {
	manager := newSessionManager()
	handler := handshake.NewHandler(manager, issuer)

	sess, _, err := manager.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)
	sess.Transition(session.StateDisconnected)

	result := handler.Resume(handshake.ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: "forged-token",
		RequestedAt: time.Now(),
	})

	if result.Accepted {
		t.Error("expected forged token to be rejected")
	}
	if result.Reason != handshake.ReasonInvalidToken {
		t.Errorf("expected reason %s, got %s", handshake.ReasonInvalidToken, result.Reason)
	}
}

func TestExpiredSessionResumeRejected(t *testing.T) {
	manager := newSessionManager()
	handler := handshake.NewHandler(manager, issuer)

	sess, _, err := manager.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)
	sess.Transition(session.StateDisconnected)
	sess.CreatedAt = time.Now().Add(-10 * time.Minute)

	result := handler.Resume(handshake.ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: issuer.Issue(sess.ID),
		RequestedAt: time.Now(),
	})

	if result.Accepted {
		t.Error("expected expired session to be rejected")
	}
	if result.Reason != handshake.ReasonSessionExpired {
		t.Errorf("expected reason %s, got %s", handshake.ReasonSessionExpired, result.Reason)
	}
}
