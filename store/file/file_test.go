package file

import (
	"os"
	"testing"

	"github.com/risa-org/scl/session"
)

// tempPath returns a temp file path and registers cleanup.
func tempPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "scl-test-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	f.Close()
	os.Remove(f.Name()) // start with no file
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func TestCreateAndGet(t *testing.T) {
	store, err := New(tempPath(t))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	sess, seq, err := store.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	got, gotSeq, ok := store.Get(sess.ID)
	if !ok {
		t.Fatal("expected to find session after creating it")
	}
	if got.ID != sess.ID {
		t.Errorf("expected ID %s, got %s", sess.ID, got.ID)
	}
	if seq == nil || gotSeq == nil {
		t.Error("expected non-nil sequencers")
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	path := tempPath(t)

	store1, err := New(path)
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}

	sess, seq, err := store1.Create(session.Interactive)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// simulate message delivery
	seq.Validate(1)
	seq.Validate(2)
	seq.Validate(3)

	// explicit flush — as would happen on disconnect
	if err := store1.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	// simulate restart
	store2, err := New(path)
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}

	got, gotSeq, ok := store2.Get(sess.ID)
	if !ok {
		t.Fatal("expected session to survive restart")
	}
	if got.ID != sess.ID {
		t.Errorf("expected ID %s, got %s", sess.ID, got.ID)
	}
	if gotSeq.LastDelivered() != 3 {
		t.Errorf("expected lastDelivered 3 after restart, got %d", gotSeq.LastDelivered())
	}
}

func TestDeleteRemovesFromDisk(t *testing.T) {
	path := tempPath(t)

	store1, _ := New(path)
	sess, _, _ := store1.Create(session.Interactive)
	store1.Delete(sess.ID)

	// reload — session should be gone
	store2, _ := New(path)
	_, _, ok := store2.Get(sess.ID)
	if ok {
		t.Error("expected deleted session to be gone after reload")
	}
}

func TestCountReflectsPersistedSessions(t *testing.T) {
	path := tempPath(t)

	store1, _ := New(path)
	store1.Create(session.Interactive)
	store1.Create(session.Interactive)

	// reload
	store2, _ := New(path)
	if store2.Count() != 2 {
		t.Errorf("expected count 2 after reload, got %d", store2.Count())
	}
}

func TestEmptyFileOnFreshStart(t *testing.T) {
	store, err := New(tempPath(t))
	if err != nil {
		t.Fatalf("unexpected error on fresh start: %v", err)
	}
	if store.Count() != 0 {
		t.Errorf("expected empty store on fresh start, got %d", store.Count())
	}
}
