package tcp

import (
	"net"
	"testing"
	"time"

	"github.com/risa-org/scl/transport"
)

// dialPair creates two connected TCP adapters — client and server.
// Uses net.Pipe() which gives us an in-memory TCP-like connection,
// no actual network ports needed. Perfect for testing.
func dialPair(t *testing.T) (*Adapter, *Adapter) {
	t.Helper()
	server, client := net.Pipe()
	return New(server), New(client)
}

func TestSendAndReceive(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()
	defer client.Close()

	// send from client
	err := client.Send(transport.Message{
		Seq:     1,
		Payload: []byte("hello from client"),
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// receive on server
	select {
	case msg := <-server.Receive():
		if msg.Seq != 1 {
			t.Errorf("expected Seq 1, got %d", msg.Seq)
		}
		if string(msg.Payload) != "hello from client" {
			t.Errorf("expected payload 'hello from client', got '%s'", msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestMultipleMessages(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()
	defer client.Close()

	// send 5 messages
	for i := uint64(1); i <= 5; i++ {
		err := client.Send(transport.Message{
			Seq:     i,
			Payload: []byte("msg"),
		})
		if err != nil {
			t.Fatalf("Send %d failed: %v", i, err)
		}
	}

	// receive all 5 in order
	for i := uint64(1); i <= 5; i++ {
		select {
		case msg := <-server.Receive():
			if msg.Seq != i {
				t.Errorf("expected Seq %d, got %d", i, msg.Seq)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message %d", i)
		}
	}
}

func TestDisconnectSignal(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()

	// close client — server should detect this
	client.Close()

	select {
	case event := <-server.Disconnected():
		if event.Reason != transport.ReasonClosedClean {
			t.Errorf("expected ReasonClosedClean, got %v", event.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for disconnect signal")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	server, client := dialPair(t)
	defer client.Close()
	defer server.Close()

	// closing multiple times should not panic or error
	server.Close()
	server.Close()
	server.Close()
}

func TestSendOnClosedReturnsError(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()

	client.Close()

	// sending on a closed connection should return ErrTransportClosed
	err := client.Send(transport.Message{Seq: 1, Payload: []byte("test")})
	if err == nil {
		t.Error("expected error sending on closed connection, got nil")
	}
}
