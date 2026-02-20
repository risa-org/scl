package session

const DefaultWindowSize uint64 = 64
const DefaultBufferSize int = 256

// Sequencer handles outgoing message numbering and incoming message validation.
// It lives inside a Session and is the single source of truth for ordering.
//
// Incoming messages are validated against a sliding window with a reorder buffer.
// Out-of-order messages within the window are held in a pending map and delivered
// in sequence order — so seq 5 arriving before seq 3 does not cause seq 3 to be
// lost. This makes the sequencer correct for any transport, not just ordered ones.
//
// Outgoing messages are assigned monotonic sequence numbers and stored in a
// fixed-size ring buffer for retransmission on resume. The ring buffer uses
// a fixed backing array with head/tail indices — no GC pressure from re-slicing.
type Sequencer struct {
	nextSeq       uint64          // next number to assign to an outgoing message
	lastDelivered uint64          // highest contiguous seq delivered — the window's left edge
	windowSize    uint64          // how far ahead we will accept, bounds memory
	pending       map[uint64]bool // received out-of-order seqs waiting for gap to fill
	drained       []uint64        // seqs drained by the last drainPending call — returned by FlushPending
	outbound      *ringBuffer     // sent-but-not-yet-acked messages, for retransmission
}

// NewSequencer creates a sequencer with default window size.
func NewSequencer() *Sequencer {
	return &Sequencer{
		nextSeq:       1,
		lastDelivered: 0,
		windowSize:    DefaultWindowSize,
		pending:       make(map[uint64]bool),
		drained:       make([]uint64, 0, 16),
		outbound:      newRingBuffer(DefaultBufferSize),
	}
}

// Next assigns a sequence number to an outgoing message.
// Numbers are monotonic and never repeat.
func (sq *Sequencer) Next() uint64 {
	seq := sq.nextSeq
	sq.nextSeq++
	return seq
}

// Sent records a message in the outbound buffer after a confirmed send.
// Only called by Sender after the transport successfully delivered the message.
func (sq *Sequencer) Sent(seq uint64, payload []byte) {
	sq.outbound.store(seq, payload)
}

// SentMessage is a message held in the outbound buffer.
type SentMessage struct {
	Seq     uint64
	Payload []byte
}

// Retransmit returns all buffered messages with seq > fromSeq, in order.
// Returns the messages and whether full recovery is possible.
// If full is false, the ring buffer has rolled over — some messages between
// fromSeq and OldestRecoverable() are permanently gone.
func (sq *Sequencer) Retransmit(fromSeq uint64) ([]SentMessage, bool) {
	return sq.outbound.since(fromSeq)
}

// Ack notifies the sequencer that the remote peer has received all messages
// up to and including seq. Messages with seq <= acked are trimmed from the
// outbound buffer — they no longer need to be retransmitted on resume.
//
// Call this whenever the remote peer sends an ack (e.g. during a long-lived
// session) to bound the outbound buffer size independent of disconnects.
func (sq *Sequencer) Ack(seq uint64) {
	sq.outbound.trim(seq)
}

// Returns 0 if the buffer is empty.
func (sq *Sequencer) OldestRecoverable() uint64 {
	return sq.outbound.oldestSeq()
}

// DeliveryVerdict is what Validate returns for each incoming message.
type DeliveryVerdict int

const (
	// Deliver: this message is next in sequence — deliver it now.
	// After receiving this verdict, call FlushPending() to drain any
	// previously out-of-order messages that are now unblocked.
	Deliver DeliveryVerdict = iota

	// DeliverPending: accepted and buffered, but an earlier seq has not
	// arrived yet. Hold this payload. When the gap fills, Validate on
	// the missing seq returns Deliver, and FlushPending drains the rest.
	DeliverPending

	// DropDuplicate: already delivered or already pending, discard.
	DropDuplicate

	// DropViolation: seq is beyond the window — protocol violation.
	DropViolation
)

// Validate checks an incoming message's sequence number and returns a verdict.
//
// This implementation handles out-of-order delivery correctly. If seq 5
// arrives before seq 3, seq 5 is held in the pending map (DeliverPending).
// When seq 3 arrives it returns Deliver, and FlushPending() returns [4, 5]
// if those were also pending. Seq 3 is never lost just because 5 arrived first.
//
// This makes the sequencer correct for any transport — TCP, WebSocket, UDP,
// or anything else — regardless of whether it guarantees ordering.
func (sq *Sequencer) Validate(seq uint64) DeliveryVerdict {
	// duplicate: already delivered
	if seq <= sq.lastDelivered {
		return DropDuplicate
	}
	// duplicate: already in pending set
	if sq.pending[seq] {
		return DropDuplicate
	}
	// beyond window: protocol violation
	if seq > sq.lastDelivered+sq.windowSize {
		return DropViolation
	}

	// next in sequence: deliver immediately, flush any now-unblocked pending
	if seq == sq.lastDelivered+1 {
		sq.lastDelivered = seq
		sq.drainPending()
		return Deliver
	}

	// out of order: hold until gap fills
	sq.pending[seq] = true
	return DeliverPending
}

// FlushPending returns the sequence numbers that became deliverable as a side
// effect of the most recent Validate call that returned Deliver. These are
// seqs that were held in the pending buffer and were released when a gap filled.
//
// Call this after every Deliver verdict to learn which previously out-of-order
// messages should now be delivered. The returned seqs are in ascending order.
// They have already been recorded as delivered internally.
//
// Example:
//
//	verdict := seq.Validate(msg.Seq)
//	switch verdict {
//	case session.Deliver:
//	    deliver(msg.Payload)
//	    for _, s := range seq.FlushPending() {
//	        deliver(heldPayloads[s]) // payloads stored on DeliverPending
//	    }
//	case session.DeliverPending:
//	    heldPayloads[msg.Seq] = msg.Payload
//	}
func (sq *Sequencer) FlushPending() []uint64 {
	if len(sq.drained) == 0 {
		return nil
	}
	out := make([]uint64, len(sq.drained))
	copy(out, sq.drained)
	sq.drained = sq.drained[:0]
	return out
}

// drainPending advances lastDelivered through any consecutive pending seqs.
// Records drained seqs into sq.drained so FlushPending can return them.
func (sq *Sequencer) drainPending() {
	sq.drained = sq.drained[:0] // reuse slice, clear previous
	for {
		next := sq.lastDelivered + 1
		if !sq.pending[next] {
			break
		}
		delete(sq.pending, next)
		sq.lastDelivered = next
		sq.drained = append(sq.drained, next)
	}
}

// LastDelivered returns the highest contiguous sequence number delivered.
func (sq *Sequencer) LastDelivered() uint64 {
	return sq.lastDelivered
}

// PendingCount returns the number of out-of-order messages currently held.
func (sq *Sequencer) PendingCount() int {
	return len(sq.pending)
}

// ResumeTo restores sequencer state to the agreed resume point.
// Clears the pending map — any held out-of-order messages are stale after resume.
// resumePoint may be ahead of lastDelivered (e.g. when the server's receive window
// is behind the client's ack position); in that case we simply advance forward.
func (sq *Sequencer) ResumeTo(lastDelivered uint64) error {
	sq.lastDelivered = lastDelivered
	sq.pending = make(map[uint64]bool)
	sq.drained = sq.drained[:0]
	return nil
}

// RestoreLastDelivered sets lastDelivered directly during store restoration.
// Only called when loading persisted sessions.
func (sq *Sequencer) RestoreLastDelivered(lastDelivered uint64) {
	sq.lastDelivered = lastDelivered
	sq.pending = make(map[uint64]bool)
}

// -------------------------------------------------------
// ringBuffer — fixed-size ring buffer for outbound messages
//
// Fixed backing array, head/tail indices, zero allocations on eviction.
// Memory is allocated once at construction and reused forever.
// Contrast with a slice-reslice approach which retains the old backing
// array in memory until GC collects it — at high message rates this
// causes measurable GC pressure.
// -------------------------------------------------------

type bufferedMessage struct {
	seq     uint64
	payload []byte
}

type ringBuffer struct {
	slots []bufferedMessage
	size  int
	head  int // index of oldest message
	tail  int // index where next write goes
	count int // number of valid messages
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		slots: make([]bufferedMessage, size),
		size:  size,
	}
}

// store writes a message into the ring. If full, overwrites the oldest slot.
func (r *ringBuffer) store(seq uint64, payload []byte) {
	p := make([]byte, len(payload))
	copy(p, payload)

	r.slots[r.tail] = bufferedMessage{seq: seq, payload: p}
	r.tail = (r.tail + 1) % r.size

	if r.count == r.size {
		// full: oldest slot overwritten, advance head
		r.head = (r.head + 1) % r.size
	} else {
		r.count++
	}
}

// since returns all buffered messages with seq > fromSeq, in order.
// full is false when fromSeq is before the oldest buffered message.
func (r *ringBuffer) since(fromSeq uint64) ([]SentMessage, bool) {
	if r.count == 0 {
		return nil, true
	}

	oldestSeq := r.slots[r.head].seq
	full := fromSeq >= oldestSeq-1

	var result []SentMessage
	for i := 0; i < r.count; i++ {
		slot := r.slots[(r.head+i)%r.size]
		if slot.seq > fromSeq {
			result = append(result, SentMessage{Seq: slot.seq, Payload: slot.payload})
		}
	}
	return result, full
}

// oldestSeq returns the seq of the oldest buffered message, or 0 if empty.
func (r *ringBuffer) oldestSeq() uint64 {
	if r.count == 0 {
		return 0
	}
	return r.slots[r.head].seq
}

// trim evicts all messages with seq <= upToSeq from the front of the buffer.
// This is used for mid-session acks to bound buffer size without a disconnect.
func (r *ringBuffer) trim(upToSeq uint64) {
	for r.count > 0 {
		oldest := r.slots[r.head].seq
		if oldest > upToSeq {
			break
		}
		r.slots[r.head] = bufferedMessage{} // release payload reference
		r.head = (r.head + 1) % r.size
		r.count--
	}
}
