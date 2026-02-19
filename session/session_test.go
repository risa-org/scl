package session

import (
	"testing"
	"time"
)

// TestNewSession checks that a fresh session is created correctly
func TestNewSession(t *testing.T) {
	s, err := NewSession(Interactive)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if s.ID == "" {
		t.Error("expected a session ID, got empty string")
	}
	if s.State != StateConnecting {
		t.Errorf("expected StateConnecting, got %v", s.State)
	}
	if s.LastDeliveredSeq != 0 {
		t.Errorf("expected sequence to start at 0, got %v", s.LastDeliveredSeq)
	}
	if s.ReconnectCount != 0 {
		t.Errorf("expected reconnect count 0, got %v", s.ReconnectCount)
	}
}

// TestSessionIDsAreUnique makes sure two sessions never get the same ID
func TestSessionIDsAreUnique(t *testing.T) {
	s1, _ := NewSession(Interactive)
	s2, _ := NewSession(Interactive)

	if s1.ID == s2.ID {
		t.Error("two sessions got the same ID — that should never happen")
	}
}

// TestValidTransitions walks through the happy path of a session lifecycle
func TestValidTransitions(t *testing.T) {
	s, _ := NewSession(Interactive)

	// connecting → active
	if ok := s.Transition(StateActive); !ok {
		t.Error("connecting → active should be valid")
	}

	// active → disconnected
	if ok := s.Transition(StateDisconnected); !ok {
		t.Error("active → disconnected should be valid")
	}

	// disconnected → resuming
	if ok := s.Transition(StateResuming); !ok {
		t.Error("disconnected → resuming should be valid")
	}

	// resuming → active (session is back)
	if ok := s.Transition(StateActive); !ok {
		t.Error("resuming → active should be valid")
	}

	// active → expired (session dies)
	if ok := s.Transition(StateExpired); !ok {
		t.Error("active → expired should be valid")
	}
}

// TestInvalidTransitions makes sure illegal moves are rejected
func TestInvalidTransitions(t *testing.T) {
	s, _ := NewSession(Interactive)
	s.Transition(StateActive)
	s.Transition(StateExpired)

	// expired → active should be impossible
	if ok := s.Transition(StateActive); ok {
		t.Error("expired → active should be invalid, but it was allowed")
	}

	// expired → disconnected should be impossible
	if ok := s.Transition(StateDisconnected); ok {
		t.Error("expired → disconnected should be invalid, but it was allowed")
	}
}

// TestIsExpired checks that policy TTLs are enforced
func TestIsExpired(t *testing.T) {
	s, _ := NewSession(Interactive)

	// brand new session should not be expired
	if s.IsExpired() {
		t.Error("new session should not be expired")
	}

	// manually backdate creation time to simulate TTL exceeded
	s.CreatedAt = time.Now().Add(-10 * time.Minute)

	// interactive policy is 5 minutes, so 10 minutes ago means expired
	if !s.IsExpired() {
		t.Error("session should be expired after exceeding policy lifetime")
	}
}

// TestReconnectCountTracking checks we can track reconnects for observability
func TestReconnectCountTracking(t *testing.T) {
	s, _ := NewSession(Interactive)
	s.Transition(StateActive)
	s.Transition(StateDisconnected)
	s.Transition(StateResuming)
	s.ReconnectCount++
	s.Transition(StateActive)

	if s.ReconnectCount != 1 {
		t.Errorf("expected reconnect count 1, got %v", s.ReconnectCount)
	}
}

// ADDITIONS to session/sequence_test.go — add these tests to the existing file

func TestSentAndRetransmitFullRecovery(t *testing.T) {
	sq := NewSequencer()

	// simulate sending 3 messages
	sq.Sent(1, []byte("msg1"))
	sq.Sent(2, []byte("msg2"))
	sq.Sent(3, []byte("msg3"))

	// client got up to seq 1, missed 2 and 3
	msgs, full := sq.Retransmit(1)
	if !full {
		t.Error("expected full recovery when all messages are in buffer")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages to retransmit, got %d", len(msgs))
	}
	if msgs[0].Seq != 2 || string(msgs[0].Payload) != "msg2" {
		t.Errorf("expected first retransmit to be seq 2 msg2, got seq %d %s", msgs[0].Seq, msgs[0].Payload)
	}
	if msgs[1].Seq != 3 || string(msgs[1].Payload) != "msg3" {
		t.Errorf("expected second retransmit to be seq 3 msg3, got seq %d %s", msgs[1].Seq, msgs[1].Payload)
	}
}

func TestRetransmitNothingMissed(t *testing.T) {
	sq := NewSequencer()

	sq.Sent(1, []byte("msg1"))
	sq.Sent(2, []byte("msg2"))

	// client got everything
	msgs, full := sq.Retransmit(2)
	if !full {
		t.Error("expected full recovery")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages to retransmit, got %d", len(msgs))
	}
}

func TestRetransmitEmptyBuffer(t *testing.T) {
	sq := NewSequencer()

	// nothing sent yet
	msgs, full := sq.Retransmit(0)
	if !full {
		t.Error("expected full recovery for empty buffer")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestRetransmitPartialRecoveryWhenBufferRolledOver(t *testing.T) {
	// use a tiny buffer size to force eviction
	sq := NewSequencer()
	sq.outbound = newOutboundBuffer(3) // only holds 3 messages

	// send 5 messages — first 2 will be evicted
	sq.Sent(1, []byte("msg1"))
	sq.Sent(2, []byte("msg2"))
	sq.Sent(3, []byte("msg3"))
	sq.Sent(4, []byte("msg4"))
	sq.Sent(5, []byte("msg5"))

	// client only got seq 1 — but msgs 2 was evicted from buffer
	msgs, full := sq.Retransmit(1)
	if full {
		t.Error("expected partial recovery when buffer rolled over")
	}
	if sq.OldestRecoverable() != 3 {
		t.Errorf("expected oldest recoverable to be 3, got %d", sq.OldestRecoverable())
	}
	// should still get msgs 3, 4, 5 (what's in buffer after fromSeq=1)
	if len(msgs) != 3 {
		t.Errorf("expected 3 recoverable messages, got %d", len(msgs))
	}
}
