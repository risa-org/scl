package integration

import (
	"net"
	"testing"
	"time"

	"github.com/risa-org/scl/handshake"
	"github.com/risa-org/scl/session"
	"github.com/risa-org/scl/store/memory"
	"github.com/risa-org/scl/transport"
	"github.com/risa-org/scl/transport/sender"
	tcpadapter "github.com/risa-org/scl/transport/tcp"
)

var issuer = session.NewTokenIssuer([]byte("integration-test-secret"))

// connPair returns a matched server adapter and client Sender over an in-memory pipe.
func connPair(t *testing.T, seq *session.Sequencer) (*tcpadapter.Adapter, *sender.Sender) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	return tcpadapter.New(serverConn), sender.New(seq, tcpadapter.New(clientConn))
}

// rawConnPair returns raw adapters — used to inject messages without Sender.
func rawConnPair(t *testing.T) (*tcpadapter.Adapter, *tcpadapter.Adapter) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	return tcpadapter.New(serverConn), tcpadapter.New(clientConn)
}

// -------------------------------------------------------
// TestFullSessionLifecycle
// Happy path: connect, send via Sender, validate, disconnect.
// Proves Sender correctly sequences and the sequencer validates.
// -------------------------------------------------------

func TestFullSessionLifecycle(t *testing.T) {
	store := memory.New()
	handler := handshake.NewHandler(store, issuer)

	sess, seq, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)

	server, client := connPair(t, seq)
	defer server.Close()
	defer client.Adapter().Close()

	for i := 0; i < 3; i++ {
		seqNum, err := client.Send([]byte("hello"))
		if err != nil {
			t.Fatalf("Send %d failed: %v", i+1, err)
		}
		if seqNum != uint64(i+1) {
			t.Errorf("expected seq %d, got %d", i+1, seqNum)
		}
	}

	for i := 0; i < 3; i++ {
		select {
		case msg := <-server.Receive():
			verdict := seq.Validate(msg.Seq)
			if verdict != session.Deliver {
				t.Errorf("message %d should deliver, got %v", i+1, verdict)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message %d", i+1)
		}
	}

	if seq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3, got %d", seq.LastDelivered())
	}

	if err := handler.Disconnect(sess.ID); err != nil {
		t.Fatalf("disconnect failed: %v", err)
	}
	if sess.State != session.StateDisconnected {
		t.Errorf("expected Disconnected, got %v", sess.State)
	}
}

// -------------------------------------------------------
// TestResumeAfterDisconnect
// Core guarantee: sequence numbers are continuous across disconnect/resume.
// 5 messages before disconnect, 3 after resume = lastDelivered 8.
// -------------------------------------------------------

func TestResumeAfterDisconnect(t *testing.T) {
	store := memory.New()
	handler := handshake.NewHandler(store, issuer)

	sess, seq, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)
	token := issuer.Issue(sess.ID)

	server, client := connPair(t, seq)

	for i := 0; i < 5; i++ {
		if _, err := client.Send([]byte("pre-disconnect")); err != nil {
			t.Fatalf("pre-disconnect Send %d failed: %v", i+1, err)
		}
	}
	for i := 0; i < 5; i++ {
		select {
		case msg := <-server.Receive():
			seq.Validate(msg.Seq)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out receiving message %d", i+1)
		}
	}

	lastAck := seq.LastDelivered() // = 5
	client.Adapter().Close()
	server.Close()
	handler.Disconnect(sess.ID)

	server2, client2 := connPair(t, seq)
	defer server2.Close()
	defer client2.Adapter().Close()

	result := handler.Resume(handshake.ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: lastAck,
		ResumeToken:       token,
		RequestedAt:       time.Now(),
	})

	if !result.Accepted {
		t.Fatalf("RESUME rejected: %s", result.Reason)
	}
	if sess.ReconnectCount != 1 {
		t.Errorf("expected ReconnectCount 1, got %d", sess.ReconnectCount)
	}

	for i := 0; i < 3; i++ {
		if _, err := client2.Send([]byte("post-resume")); err != nil {
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

	// 5 pre + 3 post = 8 total, continuous
	if seq.LastDelivered() != 8 {
		t.Errorf("expected lastDelivered 8 (5 pre + 3 post), got %d", seq.LastDelivered())
	}
}

// -------------------------------------------------------
// TestRetransmitOnResume
// Models a scenario where the CLIENT sends messages and the SERVER is the
// receiver that tracks lastDelivered. The session's seq is the server's
// inbound sequencer. The client sends 5 messages; the server receives and
// validates all 5 (lastDelivered=5). The client then reconnects claiming
// it only acked 3 (lastAck=3). resumePoint = min(3, 5) = 3.
// The handler returns seq 4 and 5 for retransmission.
//
// The outbound buffer on seq is populated by having the server record
// seq 1-5 as "sent" (as if the server had originally sent them). This
// is the correct model because seq.Retransmit() operates on the outbound
// buffer, which is filled by seq.Sent() — and the handshake calls
// seq.Retransmit(resumePoint) to produce the retransmit list.
// -------------------------------------------------------

func TestRetransmitOnResume(t *testing.T) {
	store := memory.New()
	handler := handshake.NewHandler(store, issuer)

	sess, seq, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)
	token := issuer.Issue(sess.ID)

	// Populate the outbound buffer: server "sent" these 5 messages to the client.
	// In a real deployment the server sends via sender.Send() which calls seq.Sent()
	// after a successful transport send. Here we call seq.Sent() directly to set up
	// the buffer state without needing a real transport round-trip.
	payloads := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	for i, p := range payloads {
		seq.Sent(uint64(i+1), []byte(p))
	}

	// Server also marks all 5 as delivered (received ack from client for 1-5).
	// This sets lastDelivered=5 so the handshake knows the server's position.
	for i := uint64(1); i <= 5; i++ {
		seq.Validate(i)
	}

	// Now simulate disconnect: client claims it only got up to seq 3.
	// It missed delta (4) and epsilon (5).
	clientLastAck := uint64(3)
	handler.Disconnect(sess.ID)

	result := handler.Resume(handshake.ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: clientLastAck,
		ResumeToken:       token,
		RequestedAt:       time.Now(),
	})

	if !result.Accepted {
		t.Fatalf("RESUME rejected: %s", result.Reason)
	}
	if result.Partial {
		t.Error("expected full recovery — buffer holds all 5 messages")
	}
	if len(result.Retransmit) != 2 {
		t.Fatalf("expected 2 messages to retransmit (delta, epsilon), got %d: %v",
			len(result.Retransmit), result.Retransmit)
	}
	if result.Retransmit[0].Seq != 4 || string(result.Retransmit[0].Payload) != "delta" {
		t.Errorf("expected retransmit[0] = seq 4 'delta', got seq %d %q",
			result.Retransmit[0].Seq, result.Retransmit[0].Payload)
	}
	if result.Retransmit[1].Seq != 5 || string(result.Retransmit[1].Payload) != "epsilon" {
		t.Errorf("expected retransmit[1] = seq 5 'epsilon', got seq %d %q",
			result.Retransmit[1].Seq, result.Retransmit[1].Payload)
	}

	// Now actually deliver the retransmitted messages over a new connection
	// and verify the client receives them.
	serverConn, clientConn := net.Pipe()
	serverRaw := tcpadapter.New(serverConn)
	clientRaw := tcpadapter.New(clientConn)
	defer serverRaw.Close()
	defer clientRaw.Close()

	for _, m := range result.Retransmit {
		serverRaw.Send(transport.Message{Seq: m.Seq, Payload: m.Payload})
	}

	received := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case msg := <-clientRaw.Receive():
			received = append(received, string(msg.Payload))
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for retransmitted message %d", i+1)
		}
	}
	if len(received) != 2 || received[0] != "delta" || received[1] != "epsilon" {
		t.Errorf("expected [delta epsilon] from retransmit, got %v", received)
	}
}

// -------------------------------------------------------
// TestOutOfOrderDelivery
// Injects messages out of order via raw adapters (no Sender).
// Proves the reorder buffer works for any transport.
// -------------------------------------------------------

func TestOutOfOrderDelivery(t *testing.T) {
	store := memory.New()
	handler := handshake.NewHandler(store, issuer)

	sess, seq, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)
	defer handler.Disconnect(sess.ID)

	server, client := rawConnPair(t)
	defer server.Close()
	defer client.Close()

	// inject messages out of order: 3, 1, 2
	client.Send(transport.Message{Seq: 3, Payload: []byte("third")})
	client.Send(transport.Message{Seq: 1, Payload: []byte("first")})
	client.Send(transport.Message{Seq: 2, Payload: []byte("second")})

	type result struct {
		seq     uint64
		verdict session.DeliveryVerdict
	}
	results := make([]result, 3)

	for i := 0; i < 3; i++ {
		select {
		case msg := <-server.Receive():
			verdict := seq.Validate(msg.Seq)
			results[i] = result{seq: msg.Seq, verdict: verdict}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message %d", i+1)
		}
	}

	// seq 3 arrives first — out of order, pending
	if results[0].seq != 3 || results[0].verdict != session.DeliverPending {
		t.Errorf("expected seq 3 DeliverPending, got seq %d verdict %v",
			results[0].seq, results[0].verdict)
	}
	// seq 1 arrives — next expected, delivers (seq 2 still missing so seq 3 stays pending)
	if results[1].seq != 1 || results[1].verdict != session.Deliver {
		t.Errorf("expected seq 1 Deliver, got seq %d verdict %v",
			results[1].seq, results[1].verdict)
	}
	// seq 2 arrives — next expected, delivers; seq 3 is now drained
	if results[2].seq != 2 || results[2].verdict != session.Deliver {
		t.Errorf("expected seq 2 Deliver, got seq %d verdict %v",
			results[2].seq, results[2].verdict)
	}

	if seq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3, got %d", seq.LastDelivered())
	}
	if seq.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", seq.PendingCount())
	}
}

// -------------------------------------------------------
// TestSenderDoesNotBufferFailedSend
// Failed sends must not appear in the outbound buffer.
// -------------------------------------------------------

func TestSenderDoesNotBufferFailedSend(t *testing.T) {
	store := memory.New()
	_, seq, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	serverConn.Close()

	s := sender.New(seq, tcpadapter.New(clientConn))
	defer s.Adapter().Close()

	_, sendErr := s.Send([]byte("this will fail"))
	if sendErr == nil {
		t.Skip("send did not fail — platform pipe behavior, skipping")
	}

	msgs, _ := seq.Retransmit(0)
	if len(msgs) != 0 {
		t.Errorf("failed send must not be buffered, found %d buffered messages", len(msgs))
	}
}

// -------------------------------------------------------
// TestForgedTokenRejected
// Security: forged token rejected even with valid session ID.
// -------------------------------------------------------

func TestForgedTokenRejected(t *testing.T) {
	store := memory.New()
	handler := handshake.NewHandler(store, issuer)

	sess, _, err := store.Create(session.Interactive)
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

// -------------------------------------------------------
// TestExpiredSessionResumeRejected
// TTL: expired sessions cannot resume regardless of token validity.
// -------------------------------------------------------

func TestExpiredSessionResumeRejected(t *testing.T) {
	store := memory.New()
	handler := handshake.NewHandler(store, issuer)

	sess, _, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.Transition(session.StateActive)
	sess.Transition(session.StateDisconnected)
	sess.CreatedAt = time.Now().Add(-10 * time.Minute) // force expiry

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