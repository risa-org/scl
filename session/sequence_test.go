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

func TestValidateDeliver(t *testing.T) {
	sq := NewSequencer()

	// seq 1 should be delivered cleanly
	verdict := sq.Validate(1)
	if verdict != Deliver {
		t.Errorf("expected Deliver for seq 1, got %v", verdict)
	}

	// last delivered should now be 1
	if sq.LastDelivered() != 1 {
		t.Errorf("expected lastDelivered 1, got %d", sq.LastDelivered())
	}
}

func TestValidateDropDuplicate(t *testing.T) {
	sq := NewSequencer()

	sq.Validate(1) // deliver it once

	// delivering again should be a duplicate
	verdict := sq.Validate(1)
	if verdict != DropDuplicate {
		t.Errorf("expected DropDuplicate for repeated seq 1, got %v", verdict)
	}
}

func TestValidateDropOldMessages(t *testing.T) {
	sq := NewSequencer()

	sq.Validate(5) // deliver seq 5, window advances

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

	// window is 64, so seq 65 from a lastDelivered of 0 is a violation
	verdict := sq.Validate(65)
	if verdict != DropViolation {
		t.Errorf("expected DropViolation for seq 65, got %v", verdict)
	}
}

func TestValidateWindowEdge(t *testing.T) {
	// test 1: seq exactly at window edge from zero should deliver
	sq1 := NewSequencer()
	verdict := sq1.Validate(64)
	if verdict != Deliver {
		t.Errorf("expected Deliver for seq 64 (window edge), got %v", verdict)
	}

	// test 2: seq one beyond window from zero should violate
	sq2 := NewSequencer()
	verdict = sq2.Validate(65)
	if verdict != DropViolation {
		t.Errorf("expected DropViolation for seq 65 (beyond window), got %v", verdict)
	}
}

func TestResumeTo(t *testing.T) {
	sq := NewSequencer()

	// simulate receiving and delivering messages 1 through 3
	sq.Validate(1)
	sq.Validate(2)
	sq.Validate(3)

	// resume to 2 — client got up to 2, server delivered up to 3
	err := sq.ResumeTo(2)
	if err != nil {
		t.Errorf("expected no error on valid resume, got: %v", err)
	}
	if sq.LastDelivered() != 2 {
		t.Errorf("expected lastDelivered 2 after resume, got %d", sq.LastDelivered())
	}
}

func TestResumeToInvalidPoint(t *testing.T) {
	sq := NewSequencer()

	sq.Validate(5) // lastDelivered is now 5

	// resuming to 6 — ahead of what was delivered — should fail
	err := sq.ResumeTo(6)
	if err == nil {
		t.Error("expected error when resuming ahead of last delivered, got nil")
	}
}
