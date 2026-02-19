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

// ------------------------------------------------------------
// SessionManager — simple in-memory store that ties together
// session creation, sequencer management, and lookup.
// Glue between the handshake and the session layer.
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
	return &SessionManager{
		entries: make(map[string]*entry),
	}
}

// Create makes a new session and stores it.
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

// Get satisfies the handshake.SessionStore interface.
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

// connPair returns two connected TCP adapters via an in-memory pipe.
// No real network ports needed — fast and self-contained.
func connPair(t *testing.T) (*tcpadapter.Adapter, *tcpadapter.Adapter) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	return tcpadapter.New(serverConn), tcpadapter.New(clientConn)
}

// ------------------------------------------------------------
// Tests
// ------------------------------------------------------------

// TestFullSessionLifecycle tests the happy path:
// connect → send messages → disconnect → verify state
func TestFullSessionLifecycle(t *testing.T) {
	manager := newSessionManager()
	handler := handshake.NewHandler(manager)

	// create a session and activate it
	sess, seq, err := manager.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)

	// connect client and server over in-memory TCP
	server, client := connPair(t)
	defer server.Close()
	defer client.Close()

	// send three messages from client to server
	for i := uint64(1); i <= 3; i++ {
		err := client.Send(transport.Message{
			Seq:     seq.Next(),
			Payload: []byte("hello"),
		})
		if err != nil {
			t.Fatalf("Send %d failed: %v", i, err)
		}
	}

	// receive and validate all three on server side
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

	// simulate disconnect
	err = handler.Disconnect(sess.ID)
	if err != nil {
		t.Fatalf("disconnect failed: %v", err)
	}

	if sess.State != session.StateDisconnected {
		t.Errorf("expected Disconnected, got %v", sess.State)
	}
}

// TestResumeAfterDisconnect tests the full RESUME flow:
// connect → send → disconnect → reconnect → RESUME → continue
// This is the core guarantee of the entire project.
func TestResumeAfterDisconnect(t *testing.T) {
	manager := newSessionManager()
	handler := handshake.NewHandler(manager)

	// create and activate session
	sess, seq, err := manager.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)

	// first connection
	server, client := connPair(t)

	// send 5 messages — simulate real traffic before disconnect
	for i := 0; i < 5; i++ {
		client.Send(transport.Message{
			Seq:     seq.Next(),
			Payload: []byte("pre-disconnect"),
		})
	}

	// drain server side — server receives and validates all 5
	for i := 0; i < 5; i++ {
		select {
		case msg := <-server.Receive():
			seq.Validate(msg.Seq)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out receiving message %d", i+1)
		}
	}

	// record what server last delivered before disconnect
	lastDeliveredBeforeDisconnect := seq.LastDelivered()

	// simulate abrupt disconnect — drop both sides
	client.Close()
	server.Close()

	// mark session as disconnected in the state machine
	handler.Disconnect(sess.ID)

	// --- reconnection ---

	// new TCP connection, same session identity
	server2, client2 := connPair(t)
	defer server2.Close()
	defer client2.Close()

	// client sends RESUME — proving it owns the session
	result := handler.Resume(handshake.ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: lastDeliveredBeforeDisconnect,
		ResumeToken:       sess.ID,
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

	// send post-resume messages on the new connection
	for i := 0; i < 3; i++ {
		err := client2.Send(transport.Message{
			Seq:     seq.Next(),
			Payload: []byte("post-resume"),
		})
		if err != nil {
			t.Fatalf("post-resume Send %d failed: %v", i+1, err)
		}
	}

	// receive post-resume messages — sequence continues uninterrupted
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

	// sequence numbers must be continuous — 5 pre + 3 post = 8 total
	// no gaps, no resets, no duplicates
	if seq.LastDelivered() != 8 {
		t.Errorf("expected lastDelivered 8 (5 pre + 3 post), got %d", seq.LastDelivered())
	}
}

// TestExpiredSessionResumeRejected proves TTL enforcement is real —
// expired sessions cannot be resumed under any circumstances.
func TestExpiredSessionResumeRejected(t *testing.T) {
	manager := newSessionManager()
	handler := handshake.NewHandler(manager)

	sess, _, err := manager.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)
	sess.Transition(session.StateDisconnected)

	// backdate to simulate TTL exceeded
	sess.CreatedAt = time.Now().Add(-10 * time.Minute)

	result := handler.Resume(handshake.ResumeRequest{
		SessionID:   sess.ID,
		ResumeToken: sess.ID,
		RequestedAt: time.Now(),
	})

	if result.Accepted {
		t.Error("expected expired session to be rejected")
	}
	if result.Reason != handshake.ReasonSessionExpired {
		t.Errorf("expected reason %s, got %s", handshake.ReasonSessionExpired, result.Reason)
	}
}
