package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/risa-org/scl/transport"
	"nhooyr.io/websocket"
)

// dialPair creates a connected client/server WebSocket pair
// using an in-process HTTP test server.
func dialPair(t *testing.T) (*Adapter, *Adapter) {
	t.Helper()

	// channel to hand the server-side connection to the test
	serverConnCh := make(chan *websocket.Conn, 1)

	// spin up a test HTTP server that upgrades to WebSocket
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("server accept failed: %v", err)
			return
		}
		serverConnCh <- conn
	}))
	t.Cleanup(srv.Close)

	// dial from client side
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("client dial failed: %v", err)
	}

	serverConn := <-serverConnCh

	return New(serverConn), New(clientConn)
}

func TestWebSocketSendAndReceive(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()
	defer client.Close()

	err := client.Send(transport.Message{
		Seq:     1,
		Payload: []byte("hello over websocket"),
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case msg := <-server.Receive():
		if msg.Seq != 1 {
			t.Errorf("expected Seq 1, got %d", msg.Seq)
		}
		if string(msg.Payload) != "hello over websocket" {
			t.Errorf("expected payload 'hello over websocket', got '%s'", msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestWebSocketMultipleMessages(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()
	defer client.Close()

	for i := uint64(1); i <= 5; i++ {
		err := client.Send(transport.Message{
			Seq:     i,
			Payload: []byte("msg"),
		})
		if err != nil {
			t.Fatalf("Send %d failed: %v", i, err)
		}
	}

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

func TestWebSocketDisconnectSignal(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()

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

func TestWebSocketCloseIsIdempotent(t *testing.T) {
	server, client := dialPair(t)
	defer client.Close()
	defer server.Close()

	server.Close()
	server.Close()
	server.Close()
}

func TestWebSocketSendOnClosedReturnsError(t *testing.T) {
	server, client := dialPair(t)
	defer server.Close()

	client.Close()
	time.Sleep(50 * time.Millisecond)

	err := client.Send(transport.Message{Seq: 1, Payload: []byte("test")})
	if err == nil {
		t.Error("expected error sending on closed connection, got nil")
	}
}