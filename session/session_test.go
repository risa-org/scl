package session

import (
	"testing"
	"time"
)

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

func TestSessionIDsAreUnique(t *testing.T) {
	s1, _ := NewSession(Interactive)
	s2, _ := NewSession(Interactive)
	if s1.ID == s2.ID {
		t.Error("two sessions got the same ID — that should never happen")
	}
}

func TestValidTransitions(t *testing.T) {
	s, _ := NewSession(Interactive)
	if ok := s.Transition(StateActive); !ok {
		t.Error("connecting → active should be valid")
	}
	if ok := s.Transition(StateDisconnected); !ok {
		t.Error("active → disconnected should be valid")
	}
	if ok := s.Transition(StateResuming); !ok {
		t.Error("disconnected → resuming should be valid")
	}
	if ok := s.Transition(StateActive); !ok {
		t.Error("resuming → active should be valid")
	}
	if ok := s.Transition(StateExpired); !ok {
		t.Error("active → expired should be valid")
	}
}

func TestInvalidTransitions(t *testing.T) {
	s, _ := NewSession(Interactive)
	s.Transition(StateActive)
	s.Transition(StateExpired)
	if ok := s.Transition(StateActive); ok {
		t.Error("expired → active should be invalid, but it was allowed")
	}
	if ok := s.Transition(StateDisconnected); ok {
		t.Error("expired → disconnected should be invalid, but it was allowed")
	}
}

func TestIsExpired(t *testing.T) {
	s, _ := NewSession(Interactive)
	if s.IsExpired() {
		t.Error("new session should not be expired")
	}
	s.CreatedAt = time.Now().Add(-10 * time.Minute)
	if !s.IsExpired() {
		t.Error("session should be expired after exceeding policy lifetime")
	}
}

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
