package transport

import "errors"

// ErrTransportClosed is returned when you try to send on a closed transport.
// Named errors like this let callers check the exact cause with errors.Is()
// instead of comparing raw strings — which is fragile and breaks easily.
var ErrTransportClosed = errors.New("transport closed")

// Message is what flows through a transport.
// It carries the raw bytes of a payload plus the sequence number
// assigned by the Sequencer. The transport doesn't interpret either —
// it just moves them from one side to the other.
type Message struct {
	Seq     uint64 // monotonic sequence number, assigned by Sequencer
	Payload []byte // raw application data, transport doesn't care what's in here
}

// DisconnectReason tells the session layer why a transport closed.
// This feeds directly into observability — you can see in logs whether
// a session dropped due to a network error, a timeout, or a clean close.
type DisconnectReason int

const (
	ReasonUnknown      DisconnectReason = iota // catch-all, should be rare
	ReasonNetworkError                         // underlying connection failed
	ReasonTimeout                              // no activity within deadline
	ReasonClosedClean                          // graceful shutdown by either side
)

// DisconnectEvent is sent on the channel returned by Disconnected().
// It bundles the reason with an optional error for debugging.
type DisconnectEvent struct {
	Reason DisconnectReason
	Err    error // nil on clean close, populated on errors
}

// Adapter is the contract every transport must satisfy.
// The session layer only ever talks to this interface —
// it never imports tcp, quic, websocket, or anything concrete.
//
// This is how you get "same core logic, swappable backends."
type Adapter interface {
	// Send delivers a message to the remote side.
	// Returns ErrTransportClosed if the transport is no longer active.
	// The transport guarantees reliable, ordered delivery — it does not
	// reimplement reliability, it just requires the underlying transport has it.
	Send(msg Message) error

	// Receive returns a channel that emits incoming messages.
	// The channel is closed when the transport closes.
	// Callers should range over this channel and stop when it closes.
	Receive() <-chan Message

	// Disconnected returns a channel that emits exactly one DisconnectEvent
	// when the transport closes, for any reason.
	// This is how the session layer knows to transition to StateDisconnected.
	Disconnected() <-chan DisconnectEvent

	// Close shuts down the transport cleanly.
	// Safe to call multiple times — subsequent calls are no-ops.
	Close() error
}
