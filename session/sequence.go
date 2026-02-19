package session

import "fmt"

const DefaultWindowSize uint64 = 64

// Sequencer handles outgoing message numbering and incoming message validation.
// It lives inside a Session and is the single source of truth for ordering.
type Sequencer struct {
	nextSeq       uint64 // next number to assign to an outgoing message
	lastDelivered uint64 // highest seq we've delivered, the window's left edge
	windowSize    uint64 // how far ahead we'll accept, bounds memory
}

// NewSequencer creates a sequencer with default window size.
// Both counters start at 0 — no messages delivered yet.
func NewSequencer() *Sequencer {
	return &Sequencer{
		nextSeq:       1, // sequence numbers start at 1, 0 means "nothing delivered"
		lastDelivered: 0,
		windowSize:    DefaultWindowSize,
	}
}

// Next assigns a sequence number to an outgoing message.
// Call this once per message you're about to send. Numbers never repeat.
func (sq *Sequencer) Next() uint64 {
	seq := sq.nextSeq
	sq.nextSeq++
	return seq
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
