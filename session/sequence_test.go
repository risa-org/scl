package session

import "testing"

func TestSequencerStartsAtOne(t *testing.T) {
	sq := NewSequencer()
	first := sq.Next()
	if first != 1 {
		t.Errorf("expected first sequence number to be 1, got %d", first)
	}
	second := sq.Next()
	if second != 2 {
		t.Errorf("expected second sequence number to be 2, got %d", second)
	}
}

func TestSequencerMonotonic(t *testing.T) {
	sq := NewSequencer()
	prev := sq.Next()
	for i := 0; i < 100; i++ {
		next := sq.Next()
		if next <= prev {
			t.Errorf("sequence went backwards: %d then %d", prev, next)
		}
		prev = next
	}
}

// --- In-order delivery ---

func TestValidateDeliver(t *testing.T) {
	sq := NewSequencer()
	verdict := sq.Validate(1)
	if verdict != Deliver {
		t.Errorf("expected Deliver for seq 1, got %v", verdict)
	}
	if sq.LastDelivered() != 1 {
		t.Errorf("expected lastDelivered 1, got %d", sq.LastDelivered())
	}
}

func TestValidateDropDuplicate(t *testing.T) {
	sq := NewSequencer()
	sq.Validate(1)
	verdict := sq.Validate(1)
	if verdict != DropDuplicate {
		t.Errorf("expected DropDuplicate for repeated seq 1, got %v", verdict)
	}
}

func TestValidateDropOldMessages(t *testing.T) {
	sq := NewSequencer()
	// deliver 1 through 5 in order so lastDelivered = 5
	for i := uint64(1); i <= 5; i++ {
		sq.Validate(i)
	}
	// anything at or below 5 is now a duplicate
	for _, old := range []uint64{1, 2, 3, 4, 5} {
		verdict := sq.Validate(old)
		if verdict != DropDuplicate {
			t.Errorf("expected DropDuplicate for seq %d, got %v", old, verdict)
		}
	}
}

func TestValidateDropViolation(t *testing.T) {
	sq := NewSequencer()
	// window is 64, so seq 65 from lastDelivered=0 is a violation
	verdict := sq.Validate(65)
	if verdict != DropViolation {
		t.Errorf("expected DropViolation for seq 65, got %v", verdict)
	}
}

func TestValidateWindowEdge(t *testing.T) {
	// seq exactly at window edge (lastDelivered=0, window=64, so seq 64 is valid)
	// but with the reorder buffer seq 64 is out-of-order — it goes to pending
	// the window edge test is about acceptance, not immediate delivery
	sq1 := NewSequencer()
	verdict := sq1.Validate(64)
	if verdict != DeliverPending {
		// seq 64 is within window but out of order — should be held, not violated
		t.Errorf("expected DeliverPending for seq 64 (within window, out of order), got %v", verdict)
	}

	// seq 65 is beyond the window — violation regardless of order
	sq2 := NewSequencer()
	verdict = sq2.Validate(65)
	if verdict != DropViolation {
		t.Errorf("expected DropViolation for seq 65 (beyond window), got %v", verdict)
	}

	// seq 64 arriving in order (after 1-63) should deliver
	sq3 := NewSequencer()
	for i := uint64(1); i <= 63; i++ {
		sq3.Validate(i)
	}
	verdict = sq3.Validate(64)
	if verdict != Deliver {
		t.Errorf("expected Deliver for seq 64 when arriving in order, got %v", verdict)
	}
}

// --- Out-of-order delivery (reorder buffer) ---

func TestValidateOutOfOrderReturnsDeliverPending(t *testing.T) {
	sq := NewSequencer()
	verdict := sq.Validate(2) // seq 2 before seq 1
	if verdict != DeliverPending {
		t.Errorf("expected DeliverPending for out-of-order seq 2, got %v", verdict)
	}
	if sq.LastDelivered() != 0 {
		t.Errorf("expected lastDelivered to stay 0, got %d", sq.LastDelivered())
	}
	if sq.PendingCount() != 1 {
		t.Errorf("expected 1 pending, got %d", sq.PendingCount())
	}
}

func TestValidateOutOfOrderThenGapFills(t *testing.T) {
	sq := NewSequencer()

	if v := sq.Validate(3); v != DeliverPending {
		t.Errorf("expected DeliverPending for 3, got %v", v)
	}
	if v := sq.Validate(2); v != DeliverPending {
		t.Errorf("expected DeliverPending for 2, got %v", v)
	}

	// seq 1 fills the gap — drains 2 and 3 as well
	if v := sq.Validate(1); v != Deliver {
		t.Errorf("expected Deliver when gap fills at seq 1, got %v", v)
	}
	if sq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3 after gap fills, got %d", sq.LastDelivered())
	}
	if sq.PendingCount() != 0 {
		t.Errorf("expected 0 pending after flush, got %d", sq.PendingCount())
	}
}

func TestFlushPendingReturnsReadySeqs(t *testing.T) {
	sq := NewSequencer()
	sq.Validate(3) // pending
	sq.Validate(2) // pending

	// deliver 1 — drainPending fires internally, advancing lastDelivered to 3
	// and recording [2, 3] in sq.drained for FlushPending to return
	sq.Validate(1)

	// FlushPending must return [2, 3] — the seqs that just became deliverable
	ready := sq.FlushPending()
	if len(ready) != 2 {
		t.Errorf("expected FlushPending [2 3], got %v", ready)
	}
	if len(ready) == 2 && (ready[0] != 2 || ready[1] != 3) {
		t.Errorf("expected [2 3], got %v", ready)
	}
	// lastDelivered must be 3
	if sq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3, got %d", sq.LastDelivered())
	}
	if sq.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", sq.PendingCount())
	}
	// Second call must return nothing
	if again := sq.FlushPending(); len(again) != 0 {
		t.Errorf("second FlushPending must return nothing, got %v", again)
	}
}

func TestOutOfOrderDuplicateIsDropped(t *testing.T) {
	sq := NewSequencer()
	sq.Validate(2)            // pending
	verdict := sq.Validate(2) // same seq again
	if verdict != DropDuplicate {
		t.Errorf("expected DropDuplicate for already-pending seq 2, got %v", verdict)
	}
}

func TestOutOfOrderMessageNotLost(t *testing.T) {
	sq := NewSequencer()

	// This is the core correctness test.
	// Old behavior: Validate(5) set lastDelivered=5, then Validate(3)
	// returned DropDuplicate — message 3 silently lost.
	// New behavior: out-of-order seqs go to pending, nothing is lost.

	sq.Validate(5) // pending
	sq.Validate(4) // pending
	sq.Validate(3) // pending
	sq.Validate(2) // pending

	verdict := sq.Validate(1) // fills gap — drains 2,3,4,5
	if verdict != Deliver {
		t.Errorf("expected Deliver for seq 1, got %v", verdict)
	}
	if sq.LastDelivered() != 5 {
		t.Errorf("expected lastDelivered 5 after all gaps fill, got %d", sq.LastDelivered())
	}
	if sq.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", sq.PendingCount())
	}
}

// --- ResumeTo ---

func TestResumeTo(t *testing.T) {
	sq := NewSequencer()
	sq.Validate(1)
	sq.Validate(2)
	sq.Validate(3)

	err := sq.ResumeTo(2)
	if err != nil {
		t.Errorf("expected no error on valid resume, got: %v", err)
	}
	if sq.LastDelivered() != 2 {
		t.Errorf("expected lastDelivered 2 after resume, got %d", sq.LastDelivered())
	}
}

func TestResumeToInvalidPoint(t *testing.T) {
	// Forward resume is now allowed: if the server's receive window is behind
	// the agreed resume point, we simply advance lastDelivered forward.
	// This prevents a spurious rejection when resumePoint > lastDelivered.
	sq := NewSequencer()
	sq.Validate(3) // lastDelivered = 3
	// Resume to a point ahead of lastDelivered — now valid
	err := sq.ResumeTo(5)
	if err != nil {
		t.Errorf("expected no error for forward resume, got: %v", err)
	}
	if sq.LastDelivered() != 5 {
		t.Errorf("expected lastDelivered 5 after forward resume, got %d", sq.LastDelivered())
	}
}

func TestResumeToClearsPending(t *testing.T) {
	sq := NewSequencer()
	sq.Validate(3) // pending
	sq.Validate(2) // pending

	err := sq.ResumeTo(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sq.PendingCount() != 0 {
		t.Errorf("expected pending cleared after ResumeTo, got %d", sq.PendingCount())
	}
}

// --- Outbound ring buffer ---

func TestSentAndRetransmitFullRecovery(t *testing.T) {
	sq := NewSequencer()
	sq.Sent(1, []byte("msg1"))
	sq.Sent(2, []byte("msg2"))
	sq.Sent(3, []byte("msg3"))

	msgs, full := sq.Retransmit(1)
	if !full {
		t.Error("expected full recovery")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 retransmit messages, got %d", len(msgs))
	}
	if msgs[0].Seq != 2 || string(msgs[0].Payload) != "msg2" {
		t.Errorf("expected msg2, got seq %d %s", msgs[0].Seq, msgs[0].Payload)
	}
	if msgs[1].Seq != 3 || string(msgs[1].Payload) != "msg3" {
		t.Errorf("expected msg3, got seq %d %s", msgs[1].Seq, msgs[1].Payload)
	}
}

func TestRetransmitNothingMissed(t *testing.T) {
	sq := NewSequencer()
	sq.Sent(1, []byte("msg1"))
	sq.Sent(2, []byte("msg2"))
	msgs, full := sq.Retransmit(2)
	if !full {
		t.Error("expected full recovery")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestRetransmitEmptyBuffer(t *testing.T) {
	sq := NewSequencer()
	msgs, full := sq.Retransmit(0)
	if !full {
		t.Error("expected full recovery for empty buffer")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestRingBufferEvictionAndPartialRecovery(t *testing.T) {
	sq := NewSequencer()
	sq.outbound = newRingBuffer(3)

	sq.Sent(1, []byte("msg1"))
	sq.Sent(2, []byte("msg2"))
	sq.Sent(3, []byte("msg3"))
	sq.Sent(4, []byte("msg4")) // evicts msg1
	sq.Sent(5, []byte("msg5")) // evicts msg2

	msgs, full := sq.Retransmit(1)
	if full {
		t.Error("expected partial recovery when ring rolled over")
	}
	if sq.OldestRecoverable() != 3 {
		t.Errorf("expected oldest recoverable 3, got %d", sq.OldestRecoverable())
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 recoverable messages, got %d", len(msgs))
	}
}

func TestRingBufferOrderPreserved(t *testing.T) {
	sq := NewSequencer()
	sq.outbound = newRingBuffer(4)

	sq.Sent(1, []byte("a"))
	sq.Sent(2, []byte("b"))
	sq.Sent(3, []byte("c"))
	sq.Sent(4, []byte("d"))
	sq.Sent(5, []byte("e")) // evicts 1

	msgs, _ := sq.Retransmit(1)
	expected := []uint64{2, 3, 4, 5}
	if len(msgs) != len(expected) {
		t.Fatalf("expected %d messages, got %d", len(expected), len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != expected[i] {
			t.Errorf("position %d: expected seq %d, got %d", i, expected[i], m.Seq)
		}
	}
}

func TestRingBufferFullWrap(t *testing.T) {
	sq := NewSequencer()
	sq.outbound = newRingBuffer(3)

	for i := uint64(1); i <= 9; i++ {
		sq.Sent(i, []byte("x"))
	}

	// ring holds 7, 8, 9
	msgs, full := sq.Retransmit(6)
	if !full {
		t.Error("expected full recovery from seq 6")
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Seq != 7 || msgs[1].Seq != 8 || msgs[2].Seq != 9 {
		t.Errorf("wrong seqs: %d %d %d", msgs[0].Seq, msgs[1].Seq, msgs[2].Seq)
	}
}

