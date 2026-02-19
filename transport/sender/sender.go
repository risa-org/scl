package sender

import (
	"github.com/risa-org/scl/session"
	"github.com/risa-org/scl/transport"
)

// Sender wraps a Sequencer and a transport Adapter together.
// It is the single place where outgoing messages are sequenced,
// sent, and recorded in the outbound buffer — atomically and in the right order.
//
// Before Sender existed, callers had to do three things manually:
//
//	seq := sequencer.Next()
//	adapter.Send(transport.Message{Seq: seq, Payload: payload})
//	sequencer.Sent(seq, payload) // easy to forget, called even on failed sends
//
// Sender collapses this to one call:
//
//	sender.Send(payload)
//
// Two correctness improvements over the manual approach:
//  1. Sent() is only recorded if the transport send succeeds. A failed send
//     is not buffered — no phantom messages in the retransmit queue.
//  2. There is nothing to forget. The buffer is always populated correctly.
type Sender struct {
	seq     *session.Sequencer
	adapter transport.Adapter
}

// New creates a Sender that assigns sequence numbers from seq
// and delivers messages via adapter.
func New(seq *session.Sequencer, adapter transport.Adapter) *Sender {
	return &Sender{seq: seq, adapter: adapter}
}

// Send assigns a sequence number, delivers the message via the transport,
// and records it in the outbound buffer — but only if delivery succeeded.
// Returns the assigned sequence number and any transport error.
func (s *Sender) Send(payload []byte) (uint64, error) {
	seq := s.seq.Next()

	msg := transport.Message{
		Seq:     seq,
		Payload: payload,
	}

	if err := s.adapter.Send(msg); err != nil {
		// do not record in buffer — message never left
		return 0, err
	}

	// only buffer after confirmed send
	s.seq.Sent(seq, payload)

	return seq, nil
}

// Sequencer returns the underlying sequencer.
// Useful for accessing LastDelivered, Retransmit, and other sequencer state
// without breaking encapsulation of the Sender.
func (s *Sender) Sequencer() *session.Sequencer {
	return s.seq
}

// Adapter returns the underlying transport adapter.
// Useful for accessing Receive() and Disconnected() channels.
func (s *Sender) Adapter() transport.Adapter {
	return s.adapter
}
