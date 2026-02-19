package session

import "fmt"

const DefaultWindowSize uint64 = 64
const DefaultBufferSize int = 256

// Sequencer handles outgoing message numbering and incoming message validation.
// It lives inside a Session and is the single source of truth for ordering.
type Sequencer struct {
	nextSeq       uint64          // next number to assign to an outgoing message
	lastDelivered uint64          // highest seq we've delivered, the window's left edge
	windowSize    uint64          // how far ahead we'll accept, bounds memory
	outbound      *outboundBuffer // sent-but-not-yet-acked messages, for retransmission
}

// NewSequencer creates a sequencer with default window size.
// Both counters start at 0 — no messages delivered yet.
func NewSequencer() *Sequencer {
	return &Sequencer{
		nextSeq:       1, // sequence numbers start at 1, 0 means "nothing delivered"
		lastDelivered: 0,
		windowSize:    DefaultWindowSize,
		outbound:      newOutboundBuffer(DefaultBufferSize),
	}
}

// Next assigns a sequence number to an outgoing message.
// Call this once per message you're about to send. Numbers never repeat.
func (sq *Sequencer) Next() uint64 {
	seq := sq.nextSeq
	sq.nextSeq++
	return seq
}

// Sent records a message in the outbound buffer after it has been sent.
// Call this after successfully sending a message via the transport.
// This enables retransmission on resume — the buffer holds sent-but-not-acked messages.
func (sq *Sequencer) Sent(seq uint64, payload []byte) {
	sq.outbound.store(seq, payload)
}

// SentMessage is a message held in the outbound buffer.
// Returned by Retransmit so the handshake can pass missed messages back to the client.
type SentMessage struct {
	Seq     uint64
	Payload []byte
}

// Retransmit returns messages that need to be resent after a resume.
// fromSeq is the agreed resume point — everything after it needs resending.
// Returns messages to retransmit and whether full recovery is possible.
// If full is false, the buffer rolled over and some messages are unrecoverable.
// Use OldestRecoverable() to tell the client where recovery actually starts.
func (sq *Sequencer) Retransmit(fromSeq uint64) ([]SentMessage, bool) {
	msgs, full := sq.outbound.since(fromSeq)
	result := make([]SentMessage, len(msgs))
	for i, m := range msgs {
		result[i] = SentMessage{Seq: m.seq, Payload: m.payload}
	}
	return result, full
}

// OldestRecoverable returns the seq of the oldest message still in the buffer.
// Used when partial recovery occurs to tell the client the earliest recoverable point.
// Returns 0 if the buffer is empty.
func (sq *Sequencer) OldestRecoverable() uint64 {
	return sq.outbound.oldestSeq()
}

// DeliveryVerdict is what the sequencer returns when you ask
// whether an incoming message should be delivered.
type DeliveryVerdict int

const (
	Deliver       DeliveryVerdict = iota // message is valid, deliver it
	DropDuplicate                        // already delivered this one, discard
	DropViolation                        // too far ahead, protocol violation
)

// Validate checks whether an incoming message with the given seq
// should be delivered, dropped as duplicate, or rejected as a violation.
// This is the core of the at-most-once guarantee.
func (sq *Sequencer) Validate(seq uint64) DeliveryVerdict {
	// anything at or below what we've already delivered is a duplicate
	if seq <= sq.lastDelivered {
		return DropDuplicate
	}

	// anything beyond the window is a protocol violation
	// this protects against memory exhaustion and malformed clients
	if seq > sq.lastDelivered+sq.windowSize {
		return DropViolation
	}

	// valid — deliver it and advance the window
	sq.lastDelivered = seq
	return Deliver
}

// LastDelivered returns the current window position.
// Used during the RESUME handshake so both sides can resync.
func (sq *Sequencer) LastDelivered() uint64 {
	return sq.lastDelivered
}

// ResumeTo is called during reconnect to restore sequencer state.
// The resume point comes from the RESUME handshake — it's the min
// of what the client last acked and what the server last delivered.
// After this, the sequencer continues from where it left off.
func (sq *Sequencer) ResumeTo(lastDelivered uint64) error {
	// you cannot resume beyond what has already been delivered
	// resuming forward would silently skip messages
	if lastDelivered > sq.lastDelivered {
		return fmt.Errorf(
			"invalid resume point %d: ahead of last delivered %d",
			lastDelivered, sq.lastDelivered,
		)
	}
	sq.lastDelivered = lastDelivered
	return nil
}

// RestoreLastDelivered sets lastDelivered directly during store restoration.
// Only called when loading persisted sessions — not during normal operation.
func (sq *Sequencer) RestoreLastDelivered(lastDelivered uint64) {
	sq.lastDelivered = lastDelivered
}

// -------------------------------------------------------
// outboundBuffer — fixed-size circular buffer of sent messages
// -------------------------------------------------------

// bufferedMessage holds a sent message waiting for acknowledgement.
type bufferedMessage struct {
	seq     uint64
	payload []byte
}

// outboundBuffer is a fixed-size circular buffer of sent messages.
// When full, the oldest message is evicted to make room.
// This bounds memory while still enabling retransmission for recent messages.
type outboundBuffer struct {
	messages []bufferedMessage
	size     int
}

func newOutboundBuffer(size int) *outboundBuffer {
	return &outboundBuffer{
		messages: make([]bufferedMessage, 0, size),
		size:     size,
	}
}

// store adds a sent message to the buffer.
// If the buffer is full, the oldest message is evicted.
func (b *outboundBuffer) store(seq uint64, payload []byte) {
	if len(b.messages) >= b.size {
		b.messages = b.messages[1:] // evict oldest
	}
	// copy payload to avoid retaining references to caller's buffer
	p := make([]byte, len(payload))
	copy(p, payload)
	b.messages = append(b.messages, bufferedMessage{seq: seq, payload: p})
}

// since returns all buffered messages with seq > fromSeq.
// Also returns whether fromSeq is within recoverable range (full recovery).
// If fromSeq is before the oldest buffered message, full is false —
// some messages in the gap are permanently lost.
func (b *outboundBuffer) since(fromSeq uint64) ([]bufferedMessage, bool) {
	if len(b.messages) == 0 {
		return nil, true // nothing buffered, nothing to retransmit, full recovery
	}

	oldest := b.messages[0].seq

	// if fromSeq is before our oldest buffered message there's a gap —
	// we cannot recover everything the client missed
	full := fromSeq >= oldest-1

	var result []bufferedMessage
	for _, m := range b.messages {
		if m.seq > fromSeq {
			result = append(result, m)
		}
	}
	return result, full
}

// oldestSeq returns the seq number of the oldest buffered message.
// Returns 0 if the buffer is empty.
func (b *outboundBuffer) oldestSeq() uint64 {
	if len(b.messages) == 0 {
		return 0
	}
	return b.messages[0].seq
}
