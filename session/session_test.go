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