package memory

import (
	"testing"

	"github.com/risa-org/scl/session"
)

func TestCreateAndGet(t *testing.T) {
	store := New()

	sess, seq, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if sess == nil || seq == nil {
		t.Fatal("expected non-nil session and sequencer")
	}

	got, gotSeq, ok := store.Get(sess.ID)
	if !ok {
		t.Fatal("expected to find session after creating it")
	}
	if got.ID != sess.ID {
		t.Errorf("expected ID %s, got %s", sess.ID, got.ID)
	}
	if gotSeq == nil {
		t.Error("expected non-nil sequencer from Get")
	}
}

func TestGetUnknown(t *testing.T) {
	store := New()

	_, _, ok := store.Get("does-not-exist")
	if ok {
		t.Error("expected false for unknown session ID")
	}
}

func TestDelete(t *testing.T) {
	store := New()

	sess, _, _ := store.Create(session.Interactive)
	store.Delete(sess.ID)

	_, _, ok := store.Get(sess.ID)
	if ok {
		t.Error("expected session to be gone after delete")
	}
}

func TestCount(t *testing.T) {
	store := New()

	if store.Count() != 0 {
		t.Errorf("expected count 0, got %d", store.Count())
	}

	sess1, _, _ := store.Create(session.Interactive)
	store.Create(session.Interactive)

	if store.Count() != 2 {
		t.Errorf("expected count 2, got %d", store.Count())
	}

	store.Delete(sess1.ID)

	if store.Count() != 1 {
		t.Errorf("expected count 1 after delete, got %d", store.Count())
	}
}

func TestSessionsAreIndependent(t *testing.T) {
	store := New()

	sess1, seq1, _ := store.Create(session.Interactive)
	sess2, seq2, _ := store.Create(session.Interactive)

	if sess1.ID == sess2.ID {
		t.Error("two sessions should have different IDs")
	}
	if seq1 == seq2 {
		t.Error("two sessions should have independent sequencers")
	}
}
