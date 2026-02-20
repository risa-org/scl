package session

import "testing"

// -------------------------------------------------------
// FlushPending fix tests
// -------------------------------------------------------

// TestFlushPendingAfterGapFills proves the fix works end-to-end:
// When seq 3 and 2 arrive out-of-order (DeliverPending) and then seq 1 arrives
// (Deliver), FlushPending must return [2, 3] — the seqs drained by drainPending.
// Before the fix, FlushPending re-scanned pending[] after drainPending already
// cleared it, so it always returned nothing.
func TestFlushPendingAfterGapFills(t *testing.T) {
	sq := NewSequencer()

	// 3 and 2 arrive out of order — held pending
	if v := sq.Validate(3); v != DeliverPending {
		t.Fatalf("expected DeliverPending for seq 3, got %v", v)
	}
	if v := sq.Validate(2); v != DeliverPending {
		t.Fatalf("expected DeliverPending for seq 2, got %v", v)
	}

	// 1 arrives — fills the gap, drainPending advances through 2 and 3
	if v := sq.Validate(1); v != Deliver {
		t.Fatalf("expected Deliver for seq 1, got %v", v)
	}

	// FlushPending must return [2, 3] — the seqs drained as a side effect
	flushed := sq.FlushPending()
	if len(flushed) != 2 {
		t.Fatalf("expected FlushPending to return [2, 3], got %v", flushed)
	}
	if flushed[0] != 2 || flushed[1] != 3 {
		t.Errorf("expected [2 3], got %v", flushed)
	}
	if sq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3, got %d", sq.LastDelivered())
	}
	if sq.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", sq.PendingCount())
	}

	// Second call to FlushPending after same Validate must return nothing
	if flushed2 := sq.FlushPending(); len(flushed2) != 0 {
		t.Errorf("second FlushPending call should return nothing, got %v", flushed2)
	}
}

// TestFlushPendingWithPartialDrain: gap fills partially — only a subset drained.
func TestFlushPendingWithPartialDrain(t *testing.T) {
	sq := NewSequencer()

	sq.Validate(5) // pending: {5}
	sq.Validate(3) // pending: {3, 5}
	sq.Validate(2) // pending: {2, 3, 5}

	// Deliver 1 — drainPending: clears 2, 3; stops at 4 (missing). 5 stays pending.
	if v := sq.Validate(1); v != Deliver {
		t.Fatalf("expected Deliver for 1, got %v", v)
	}
	flushed := sq.FlushPending()
	if len(flushed) != 2 || flushed[0] != 2 || flushed[1] != 3 {
		t.Errorf("expected FlushPending [2 3], got %v", flushed)
	}
	if sq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3, got %d", sq.LastDelivered())
	}
	if sq.PendingCount() != 1 { // seq 5 still pending
		t.Errorf("expected 1 pending (seq 5), got %d", sq.PendingCount())
	}

	// Deliver 4 — drainPending clears 5
	if v := sq.Validate(4); v != Deliver {
		t.Fatalf("expected Deliver for 4, got %v", v)
	}
	flushed = sq.FlushPending()
	if len(flushed) != 1 || flushed[0] != 5 {
		t.Errorf("expected FlushPending [5], got %v", flushed)
	}
	if sq.LastDelivered() != 5 {
		t.Errorf("expected lastDelivered 5, got %d", sq.LastDelivered())
	}
}

// -------------------------------------------------------
// ResumeTo forward-resume test
// -------------------------------------------------------

// TestResumeToAllowsForwardResume proves that ResumeTo no longer errors
// when the resume point is ahead of lastDelivered.
// This happens when the server's receive window is behind the client's ack.
func TestResumeToAllowsForwardResume(t *testing.T) {
	sq := NewSequencer()
	// lastDelivered is 0 — server hasn't received anything from client yet.
	// But the client claims it acked seq 5 from the server.
	// resumePoint = min(5, 0) = 0, so this case is normally avoided by the handshake.
	// However, if lastDelivered=2 and client says 5 (their receive window),
	// min(5,2)=2, fine. But if somehow we call ResumeTo(5) with lastDelivered=2:
	if err := sq.ResumeTo(5); err != nil {
		t.Fatalf("ResumeTo forward should not error, got: %v", err)
	}
	if sq.LastDelivered() != 5 {
		t.Errorf("expected lastDelivered 5 after forward resume, got %d", sq.LastDelivered())
	}
	if sq.PendingCount() != 0 {
		t.Errorf("expected pending cleared, got %d", sq.PendingCount())
	}
}

// TestResumeToBackwardResume verifies backward resume still works.
func TestResumeToBackwardResume(t *testing.T) {
	sq := NewSequencer()
	for i := uint64(1); i <= 5; i++ {
		sq.Validate(i)
	}
	if sq.LastDelivered() != 5 {
		t.Fatalf("setup: expected lastDelivered 5, got %d", sq.LastDelivered())
	}
	if err := sq.ResumeTo(3); err != nil {
		t.Fatalf("ResumeTo backward should not error: %v", err)
	}
	if sq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3, got %d", sq.LastDelivered())
	}
}

// -------------------------------------------------------
// Ack / trim tests
// -------------------------------------------------------

// TestAckTrimsBuffer proves that Ack(seq) evicts all buffered messages
// with seq <= acked, so the buffer shrinks during long-lived sessions.
func TestAckTrimsBuffer(t *testing.T) {
	sq := NewSequencer()
	for i := 1; i <= 5; i++ {
		sq.Sent(uint64(i), []byte("payload"))
	}

	msgs, _ := sq.Retransmit(0)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 buffered, got %d", len(msgs))
	}

	// ack up to seq 3 — buffer should drop 1, 2, 3
	sq.Ack(3)

	// Retransmit(3): asking "what did client miss after seq 3?" = [4, 5], full recovery
	msgs, full := sq.Retransmit(3)
	if len(msgs) != 2 {
		t.Errorf("after Ack(3): expected 2 buffered (4,5), got %d", len(msgs))
	}
	if !full {
		t.Error("after Ack(3): Retransmit(3) should be full — oldest in buffer is 4, resumePoint is 3")
	}
	if len(msgs) == 2 {
		if msgs[0].Seq != 4 || msgs[1].Seq != 5 {
			t.Errorf("expected seqs [4 5], got [%d %d]", msgs[0].Seq, msgs[1].Seq)
		}
	}

	// Retransmit(0) is NOT full — messages 1-3 were trimmed and are unrecoverable
	_, fullFromZero := sq.Retransmit(0)
	if fullFromZero {
		t.Error("Retransmit(0) after Ack(3) should NOT be full — messages 1-3 are gone")
	}
}

// TestAckAllClearsBuffer proves Ack(lastSeq) empties the buffer.
func TestAckAllClearsBuffer(t *testing.T) {
	sq := NewSequencer()
	for i := 1; i <= 5; i++ {
		sq.Sent(uint64(i), []byte("payload"))
	}
	sq.Ack(5)
	msgs, full := sq.Retransmit(0)
	if len(msgs) != 0 {
		t.Errorf("after Ack(5): expected empty buffer, got %d messages", len(msgs))
	}
	if !full {
		t.Error("empty buffer should always report full recovery")
	}
}

// TestAckBeyondBufferIsHarmless proves Ack(N) where N > highest buffered is safe.
func TestAckBeyondBufferIsHarmless(t *testing.T) {
	sq := NewSequencer()
	sq.Sent(1, []byte("one"))
	sq.Sent(2, []byte("two"))
	sq.Ack(100) // far beyond what's buffered
	msgs, _ := sq.Retransmit(0)
	if len(msgs) != 0 {
		t.Errorf("expected buffer cleared, got %d messages", len(msgs))
	}
}

// TestAckBelowOldestIsHarmless proves Ack(N) where N < oldest buffered is a no-op.
func TestAckBelowOldestIsHarmless(t *testing.T) {
	sq := NewSequencer()
	sq.Sent(5, []byte("five"))
	sq.Sent(6, []byte("six"))
	sq.Ack(3) // below oldest — nothing to evict
	msgs, _ := sq.Retransmit(0)
	if len(msgs) != 2 {
		t.Errorf("Ack below oldest should be no-op, expected 2 msgs, got %d", len(msgs))
	}
}
