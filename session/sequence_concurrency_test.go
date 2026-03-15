package session

import (
	"sync"
	"testing"
)

// TestSequencerConcurrentAccess exercises mixed concurrent operations to ensure
// the sequencer stays safe under real multi-goroutine usage.
func TestSequencerConcurrentAccess(t *testing.T) {
	sq := NewSequencer()

	const senders = 8
	const sendsPerSender = 200

	var wg sync.WaitGroup
	wg.Add(senders)

	for i := 0; i < senders; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < sendsPerSender; j++ {
				seq := sq.Next()
				sq.Sent(seq, []byte("payload"))

				if seq%3 == 0 {
					sq.Ack(seq - 1)
				}

				// exercise read paths while writers are active
				sq.LastDelivered()
				sq.PendingCount()
				sq.OldestRecoverable()
				sq.Retransmit(0)
			}
		}(i)
	}

	// Concurrent inbound validation path.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); i <= 500; i++ {
			v := sq.Validate(i)
			if v == Deliver {
				sq.FlushPending()
			}
		}
	}()

	wg.Wait()

	msgs, _ := sq.Retransmit(0)
	if len(msgs) > DefaultBufferSize {
		t.Fatalf("buffer should never exceed %d entries, got %d", DefaultBufferSize, len(msgs))
	}
}
