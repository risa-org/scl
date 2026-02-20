package websocket

import (
	"context"
	"sync"

	"github.com/risa-org/scl/transport"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// Adapter implements transport.Adapter over a WebSocket connection.
// Uses JSON framing — each message is a JSON object with Seq and Payload.
// Unlike TCP, WebSocket already has message boundaries built in,
// so we don't need to implement our own framing.
type Adapter struct {
	conn       *websocket.Conn
	incoming   chan transport.Message
	disconnect chan transport.DisconnectEvent
	closeOnce  sync.Once
	ctx        context.Context
	cancel     context.CancelFunc
}

type message struct {
	Seq     uint64 `json:"seq"`
	Payload []byte `json:"payload"`
}

// New wraps an existing *websocket.Conn in a transport Adapter.
func New(conn *websocket.Conn) *Adapter {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Adapter{
		conn:       conn,
		incoming:   make(chan transport.Message, 64),
		disconnect: make(chan transport.DisconnectEvent, 1),
		ctx:        ctx,
		cancel:     cancel,
	}
	go a.readLoop()
	return a
}

func (a *Adapter) Send(msg transport.Message) error {
	err := wsjson.Write(a.ctx, a.conn, message{
		Seq:     msg.Seq,
		Payload: msg.Payload,
	})
	if err != nil {
		return transport.ErrTransportClosed
	}
	return nil
}

func (a *Adapter) Receive() <-chan transport.Message {
	return a.incoming
}

func (a *Adapter) Disconnected() <-chan transport.DisconnectEvent {
	return a.disconnect
}

func (a *Adapter) Close() error {
	var err error
	a.closeOnce.Do(func() {
		a.cancel()
		err = a.conn.Close(websocket.StatusNormalClosure, "closed")
	})
	return err
}

func (a *Adapter) readLoop() {
	defer func() {
		close(a.incoming)
		a.Close()
	}()

	for {
		var msg message
		err := wsjson.Read(a.ctx, a.conn, &msg)
		if err != nil {
			a.signalDisconnect(err)
			return
		}
		a.incoming <- transport.Message{
			Seq:     msg.Seq,
			Payload: msg.Payload,
		}
	}
}

// signalDisconnect sends exactly one disconnect event.
// StatusNormalClosure (1000) and StatusGoingAway (1001) are both clean closes —
// different WebSocket implementations and shutdown timing produce either code.
// Context cancellation means we closed it ourselves — also clean.
func (a *Adapter) signalDisconnect(err error) {
	event := transport.DisconnectEvent{}

	status := websocket.CloseStatus(err)
	switch {
	case status == websocket.StatusNormalClosure,
		status == websocket.StatusGoingAway,
		a.ctx.Err() != nil:
		event.Reason = transport.ReasonClosedClean
	default:
		event.Reason = transport.ReasonNetworkError
		event.Err = err
	}

	select {
	case a.disconnect <- event:
	default:
	}
}
