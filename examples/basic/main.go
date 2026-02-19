package main

import (
	"fmt"
	"net"
	"time"

	"github.com/risa-org/scl/handshake"
	"github.com/risa-org/scl/session"
	"github.com/risa-org/scl/store/memory"
	"github.com/risa-org/scl/transport"
	"github.com/risa-org/scl/transport/tcp"
)

func main() {
	fmt.Println("=== SCL Basic Example ===")
	fmt.Println()

	// --- Setup ---
	issuer, err := session.NewRandomTokenIssuer()
	if err != nil {
		panic(err)
	}

	store := memory.New()
	handler := handshake.NewHandler(store, issuer)

	// Create a session with Interactive policy (5 min TTL)
	sess, seq, err := store.Create(session.Interactive)
	if err != nil {
		panic(err)
	}
	sess.Transition(session.StateActive)

	// Issue signed token — client stores this for reconnect
	token := issuer.Issue(sess.ID)

	fmt.Printf("Session created\n")
	fmt.Printf("  ID:     %s\n", sess.ID)
	fmt.Printf("  Policy: %s\n", sess.Policy.Name)
	fmt.Printf("  State:  %s\n", stateName(sess.State))
	fmt.Printf("  Token:  %s...\n", token[:16])
	fmt.Println()

	// --- First connection ---
	fmt.Println("--- First Connection ---")

	serverConn, clientConn := net.Pipe()
	server := tcp.New(serverConn)
	client := tcp.New(clientConn)

	received := make(chan transport.Message, 32)
	go func() {
		for msg := range server.Receive() {
			received <- msg
		}
	}()

	for i := 0; i < 4; i++ {
		msg := transport.Message{
			Seq:     seq.Next(),
			Payload: []byte(fmt.Sprintf("message %d", i+1)),
		}
		client.Send(msg)
		fmt.Printf("  → sent   seq=%d payload=%q\n", msg.Seq, msg.Payload)
	}

	for i := 0; i < 4; i++ {
		select {
		case msg := <-received:
			verdict := seq.Validate(msg.Seq)
			fmt.Printf("  ← received seq=%d payload=%q verdict=%s\n",
				msg.Seq, msg.Payload, verdictName(verdict))
		case <-time.After(2 * time.Second):
			panic("timed out waiting for message")
		}
	}

	fmt.Printf("\n  lastDelivered=%d reconnectCount=%d\n",
		seq.LastDelivered(), sess.ReconnectCount)
	fmt.Println()

	// --- Simulate disconnect ---
	fmt.Println("--- Network Drop ---")

	lastAck := seq.LastDelivered()
	client.Close()
	server.Close()
	handler.Disconnect(sess.ID)

	fmt.Printf("  connection dropped\n")
	fmt.Printf("  session state: %s\n", stateName(sess.State))
	fmt.Printf("  last ack: %d\n", lastAck)
	fmt.Println()

	// --- Reconnect with RESUME ---
	fmt.Println("--- Reconnecting ---")

	serverConn2, clientConn2 := net.Pipe()
	server2 := tcp.New(serverConn2)
	client2 := tcp.New(clientConn2)

	go func() {
		for msg := range server2.Receive() {
			received <- msg
		}
	}()

	result := handler.Resume(handshake.ResumeRequest{
		SessionID:         sess.ID,
		LastAckFromServer: lastAck,
		ResumeToken:       token,
		RequestedAt:       time.Now(),
	})

	if !result.Accepted {
		panic(fmt.Sprintf("RESUME rejected: %s", result.Reason))
	}

	fmt.Printf("  RESUME accepted\n")
	fmt.Printf("  resume point: %d\n", result.ResumePoint)
	fmt.Printf("  session state: %s\n", stateName(sess.State))
	fmt.Printf("  reconnect count: %d\n", sess.ReconnectCount)
	fmt.Println()

	// --- Continue sending on new connection ---
	fmt.Println("--- Continuing After Resume ---")

	for i := 0; i < 3; i++ {
		msg := transport.Message{
			Seq:     seq.Next(),
			Payload: []byte(fmt.Sprintf("post-resume message %d", i+1)),
		}
		client2.Send(msg)
		fmt.Printf("  → sent   seq=%d payload=%q\n", msg.Seq, msg.Payload)
	}

	for i := 0; i < 3; i++ {
		select {
		case msg := <-received:
			verdict := seq.Validate(msg.Seq)
			fmt.Printf("  ← received seq=%d payload=%q verdict=%s\n",
				msg.Seq, msg.Payload, verdictName(verdict))
		case <-time.After(2 * time.Second):
			panic("timed out waiting for post-resume message")
		}
	}

	fmt.Println()
	fmt.Println("--- Final State ---")
	fmt.Printf("  session:       %s\n", sess.ID)
	fmt.Printf("  state:         %s\n", stateName(sess.State))
	fmt.Printf("  lastDelivered: %d\n", seq.LastDelivered())
	fmt.Printf("  reconnects:    %d\n", sess.ReconnectCount)
	fmt.Println()
	fmt.Println("Sequence numbers were continuous across the disconnect.")
	fmt.Println("No gaps. No resets. No duplicates.")
	fmt.Println("Token was HMAC-signed — session ID alone is not enough to hijack.")

	client2.Close()
	server2.Close()
}

func stateName(s session.SessionState) string {
	switch s {
	case session.StateConnecting:
		return "Connecting"
	case session.StateActive:
		return "Active"
	case session.StateDisconnected:
		return "Disconnected"
	case session.StateResuming:
		return "Resuming"
	case session.StateExpired:
		return "Expired"
	default:
		return "Unknown"
	}
}

func verdictName(v session.DeliveryVerdict) string {
	switch v {
	case session.Deliver:
		return "DELIVER"
	case session.DropDuplicate:
		return "DROP(duplicate)"
	case session.DropViolation:
		return "DROP(violation)"
	default:
		return "UNKNOWN"
	}
}
