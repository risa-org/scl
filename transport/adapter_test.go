package transport

import "testing"

// TestMessageFields checks that Message carries both seq and payload correctly.
// Simple but establishes that the struct is what we think it is.
func TestMessageFields(t *testing.T) {
	msg := Message{
		Seq:     42,
		Payload: []byte("hello"),
	}

	if msg.Seq != 42 {
		t.Errorf("expected Seq 42, got %d", msg.Seq)
	}
	if string(msg.Payload) != "hello" {
		t.Errorf("expected payload 'hello', got '%s'", msg.Payload)
	}
}

// TestDisconnectReasonConstants checks all reasons are distinct.
// iota bugs (accidentally reordering constants) would break this.
func TestDisconnectReasonConstants(t *testing.T) {
	reasons := []DisconnectReason{
		ReasonUnknown,
		ReasonNetworkError,
		ReasonTimeout,
		ReasonClosedClean,
	}

	seen := make(map[DisconnectReason]bool)
	for _, r := range reasons {
		if seen[r] {
			t.Errorf("duplicate DisconnectReason value: %d", r)
		}
		seen[r] = true
	}
}

// TestDisconnectEvent checks the event struct carries reason and error together.
func TestDisconnectEvent(t *testing.T) {
	event := DisconnectEvent{
		Reason: ReasonNetworkError,
		Err:    ErrTransportClosed,
	}

	if event.Reason != ReasonNetworkError {
		t.Errorf("expected ReasonNetworkError, got %d", event.Reason)
	}
	if event.Err != ErrTransportClosed {
		t.Errorf("expected ErrTransportClosed, got %v", event.Err)
	}
}
