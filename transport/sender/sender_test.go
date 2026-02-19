package sender

import (
	"errors"
	"testing"

	"github.com/risa-org/scl/session"
	"github.com/risa-org/scl/transport"
)

// mockAdapter is a minimal transport.Adapter for testing.
// It records sent messages and can be configured to fail.
type mockAdapter struct {
	sent      []transport.Message
	failAfter int // fail on the Nth send, -1 means never fail
	calls     int
}

func newMockAdapter() *mockAdapter {
	return &mockAdapter{failAfter: -1}
}

func (m *mockAdapter) Send(msg transport.Message) error {
	m.calls++
	if m.failAfter >= 0 && m.calls > m.failAfter {
		return transport.ErrTransportClosed
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mockAdapter) Receive() <-chan transport.Message {
	return make(chan transport.Message)
}

func (m *mockAdapter) Disconnected() <-chan transport.DisconnectEvent {
	return make(chan transport.DisconnectEvent)
}

func (m *mockAdapter) Close() error { return nil }

// --- Tests ---

func TestSendAssignsSequenceNumbers(t *testing.T) {
	seq := session.NewSequencer()
	adapter := newMockAdapter()
	s := New(seq, adapter)

	seqNum, err := s.Send([]byte("hello"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if seqNum != 1 {
		t.Errorf("expected seq 1, got %d", seqNum)
	}

	seqNum2, _ := s.Send([]byte("world"))
	if seqNum2 != 2 {
		t.Errorf("expected seq 2, got %d", seqNum2)
	}
}

func TestSendDeliversToAdapter(t *testing.T) {
	seq := session.NewSequencer()
	adapter := newMockAdapter()
	s := New(seq, adapter)

	s.Send([]byte("hello"))
	s.Send([]byte("world"))

	if len(adapter.sent) != 2 {
		t.Fatalf("expected 2 messages delivered, got %d", len(adapter.sent))
	}
	if string(adapter.sent[0].Payload) != "hello" {
		t.Errorf("expected first payload 'hello', got '%s'", adapter.sent[0].Payload)
	}
	if string(adapter.sent[1].Payload) != "world" {
		t.Errorf("expected second payload 'world', got '%s'", adapter.sent[1].Payload)
	}
}

func TestSendRecordsInBufferOnSuccess(t *testing.T) {
	seq := session.NewSequencer()
	adapter := newMockAdapter()
	s := New(seq, adapter)

	s.Send([]byte("msg1"))
	s.Send([]byte("msg2"))
	s.Send([]byte("msg3"))

	// client missed msg2 and msg3 — resume from seq 1
	msgs, full := seq.Retransmit(1)
	if !full {
		t.Error("expected full recovery")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 retransmit messages, got %d", len(msgs))
	}
	if string(msgs[0].Payload) != "msg2" {
		t.Errorf("expected msg2, got %s", msgs[0].Payload)
	}
	if string(msgs[1].Payload) != "msg3" {
		t.Errorf("expected msg3, got %s", msgs[1].Payload)
	}
}

func TestSendDoesNotRecordInBufferOnFailure(t *testing.T) {
	seq := session.NewSequencer()
	adapter := newMockAdapter()
	adapter.failAfter = 1 // first send succeeds, second fails

	s := New(seq, adapter)

	s.Send([]byte("msg1"))           // succeeds, buffered
	_, err := s.Send([]byte("msg2")) // fails, must NOT be buffered

	if err == nil {
		t.Fatal("expected error on failed send")
	}
	if !errors.Is(err, transport.ErrTransportClosed) {
		t.Errorf("expected ErrTransportClosed, got %v", err)
	}

	// only msg1 should be in buffer
	msgs, _ := seq.Retransmit(0)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 buffered message, got %d — failed send must not be buffered", len(msgs))
	}
	if string(msgs[0].Payload) != "msg1" {
		t.Errorf("expected msg1 in buffer, got %s", msgs[0].Payload)
	}
}

func TestSequencerAccessor(t *testing.T) {
	seq := session.NewSequencer()
	adapter := newMockAdapter()
	s := New(seq, adapter)

	if s.Sequencer() != seq {
		t.Error("expected Sequencer() to return the underlying sequencer")
	}
}

func TestAdapterAccessor(t *testing.T) {
	seq := session.NewSequencer()
	adapter := newMockAdapter()
	s := New(seq, adapter)

	if s.Adapter() != adapter {
		t.Error("expected Adapter() to return the underlying adapter")
	}
}
