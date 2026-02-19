package tcp

import (
	"encoding/binary"
	"io"
	"net"
	"sync"

	"github.com/risa-org/scl/transport"
)

// Adapter implements transport.Adapter over a raw TCP connection.
//
// Wire format for each message:
//
//	[8 bytes: Seq as uint64 big-endian][4 bytes: payload length uint32][N bytes: payload]
//
// We define our own simple framing because TCP is a stream protocol —
// it has no concept of message boundaries. Without framing, a Read()
// call might return half a message or two messages joined together.
// This format lets us always read exactly one message at a time.
type Adapter struct {
	conn       net.Conn                       // the underlying TCP connection
	incoming   chan transport.Message         // delivers received messages to caller
	disconnect chan transport.DisconnectEvent // signals when connection closes
	closeOnce  sync.Once                      // guarantees cleanup runs exactly once
	writeMu    sync.Mutex                     // one writer at a time, TCP is not concurrent-safe for writes
}

// New wraps an existing net.Conn in a transport Adapter.
// The conn must already be established — dialing or accepting happens outside.
// Immediately starts a read loop goroutine in the background.
func New(conn net.Conn) *Adapter {
	a := &Adapter{
		conn:       conn,
		incoming:   make(chan transport.Message, 64),        // buffered so reader doesn't block on slow consumers
		disconnect: make(chan transport.DisconnectEvent, 1), // buffered so writer never blocks
	}

	// start reading in the background immediately
	// this goroutine runs until the connection closes
	go a.readLoop()

	return a
}

// Send encodes a message and writes it to the TCP connection.
// Uses writeMu to ensure only one goroutine writes at a time.
func (a *Adapter) Send(msg transport.Message) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	// write seq as 8 bytes big-endian
	var seqBuf [8]byte
	binary.BigEndian.PutUint64(seqBuf[:], msg.Seq)
	if _, err := a.conn.Write(seqBuf[:]); err != nil {
		return transport.ErrTransportClosed
	}

	// write payload length as 4 bytes big-endian
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(msg.Payload)))
	if _, err := a.conn.Write(lenBuf[:]); err != nil {
		return transport.ErrTransportClosed
	}

	// write the actual payload
	if _, err := a.conn.Write(msg.Payload); err != nil {
		return transport.ErrTransportClosed
	}

	return nil
}

// Receive returns the channel of incoming messages.
// Range over this channel to process messages as they arrive.
// The channel is closed when the connection closes.
func (a *Adapter) Receive() <-chan transport.Message {
	return a.incoming
}

// Disconnected returns a channel that emits exactly one event when
// the connection closes, for any reason.
func (a *Adapter) Disconnected() <-chan transport.DisconnectEvent {
	return a.disconnect
}

// Close shuts down the TCP connection cleanly.
// Safe to call multiple times — cleanup runs exactly once due to sync.Once.
func (a *Adapter) Close() error {
	var err error
	a.closeOnce.Do(func() {
		err = a.conn.Close()
	})
	return err
}

// readLoop runs in a goroutine and continuously reads messages from the
// TCP connection. When the connection closes it signals disconnect and exits.
func (a *Adapter) readLoop() {
	// whatever happens, when this function returns we clean up
	defer func() {
		close(a.incoming) // signal to Receive() callers that we're done
		a.Close()         // ensure connection is closed
	}()

	for {
		// read seq — 8 bytes
		var seqBuf [8]byte
		if _, err := io.ReadFull(a.conn, seqBuf[:]); err != nil {
			a.signalDisconnect(err)
			return
		}
		seq := binary.BigEndian.Uint64(seqBuf[:])

		// read payload length — 4 bytes
		var lenBuf [4]byte
		if _, err := io.ReadFull(a.conn, lenBuf[:]); err != nil {
			a.signalDisconnect(err)
			return
		}
		payloadLen := binary.BigEndian.Uint32(lenBuf[:])

		// read payload — exactly payloadLen bytes
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(a.conn, payload); err != nil {
			a.signalDisconnect(err)
			return
		}

		// deliver to caller
		a.incoming <- transport.Message{
			Seq:     seq,
			Payload: payload,
		}
	}
}

// signalDisconnect figures out the reason for disconnection and
// sends exactly one event on the disconnect channel.
func (a *Adapter) signalDisconnect(err error) {
	event := transport.DisconnectEvent{}

	if err == nil || err == io.EOF {
		// EOF means the remote side closed cleanly
		event.Reason = transport.ReasonClosedClean
	} else {
		// anything else is a network error
		event.Reason = transport.ReasonNetworkError
		event.Err = err
	}

	// non-blocking send — channel is buffered(1) so this never blocks
	// if somehow disconnect was already sent, we don't send again
	select {
	case a.disconnect <- event:
	default:
	}
}
