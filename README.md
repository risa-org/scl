# SCL — Session Continuity Layer

A thin, transport-agnostic Go library that keeps sessions alive across network disconnects.

---

## The Problem

Every app that needs persistent connections — games, chat tools, IoT devices, mobile sync — hits the same wall:

> The network drops. The connection resets. Your app rebuilds everything from scratch.

TCP resets sequence numbers, ordering, and identity on reconnect. Every team solves this differently. Nobody solves it well. The result is complex, fragile reconnect logic scattered across every codebase.

## The Fix

SCL decouples session identity from transport lifetime:

```
transport lifetime ≠ session identity
```

A session survives across multiple underlying connections. The transport changes. The identity doesn't.

**One guarantee:**
> Ordered, at-most-once message delivery across transient disconnects — within a bounded session lifetime. Missed messages are retransmitted on resume. Out-of-order messages are held and delivered in sequence order regardless of transport.

---

## How It Works

```
Application
    ↑
Session Continuity Layer
    ↑
Transport Adapter (TCP / WebSocket / UDP / any)
    ↑
Network
```

When a client reconnects it sends a RESUME request:

```
Client → Server:  RESUME { session_id, last_ack, resume_token }
Server → Client:  RESUME_OK { resume_point, retransmit_list }
               or RESUME_REJECT { reason }
```

Resume point = `min(client_last_ack, server_last_delivered)`. Server is authoritative. Both sides resync from the last point they both agree on. Sequence numbers continue from where they left off — no gaps, no resets, no duplicates. Messages the client missed are retransmitted from the server's outbound ring buffer.

### Out-of-order delivery

The sequencer handles out-of-order delivery correctly. If seq 5 arrives before seq 3, seq 5 is held in a pending map and returns `DeliverPending`. When seq 3 arrives it returns `Deliver`, and `FlushPending()` returns any now-unblocked seqs. Seq 3 is never lost just because 5 arrived first.

This makes the sequencer correct for any transport — TCP, WebSocket, UDP, or custom — regardless of whether the transport guarantees ordering.

---

## Project Structure

```
scl/
├── session/               # Core: state machine, lifecycle, policy TTLs, token signing
├── handshake/             # RESUME protocol logic
├── transport/             # Adapter interface every transport must satisfy
│   ├── sender/            # Wraps sequencer + adapter — one call to send, sequence, and buffer
│   ├── tcp/               # TCP adapter with binary message framing
│   └── websocket/         # WebSocket adapter with JSON framing
├── store/
│   ├── memory/            # In-memory SessionStore — fast, lost on restart
│   └── file/              # File-backed SessionStore — survives restarts
├── integration/           # End-to-end tests proving the full stack works
└── examples/
    └── basic/             # Runnable example: connect, disconnect, resume
```

**Key rule:** `session/` and `handshake/` never import `transport/` or `store/`. Core logic never knows about networking or persistence. Adapters depend on core, not the other way around.

---

## Session Policies

Sessions are not promises — they expire. Three built-in policies:

| Policy | Lifetime |
|--------|----------|
| `session.Ephemeral` | 30 seconds |
| `session.Interactive` | 5 minutes |
| `session.Durable` | 2 hours |

---

## Quick Example

```go
// 1. Create a token issuer — secret key never leaves the server.
//    In production load this from an environment variable or secrets manager.
issuer, _ := session.NewRandomTokenIssuer()

// 2. Choose a store — memory for single-process, file for persistence
store := memory.New()
// or: store, _ := file.New("sessions.json")

// 3. Create a handshake handler
handler := handshake.NewHandler(store, issuer)

// 4. Create a session when client first connects
sess, seq, _ := store.Create(session.Interactive)
sess.Transition(session.StateActive)

// 5. Issue a signed token — send this to the client once at session creation.
//    The client stores it and presents it on every reconnect attempt.
token := issuer.Issue(sess.ID)

// 6. Wrap your connection in a Sender — handles seq, delivery, and buffering atomically
conn, _ := net.Dial("tcp", "localhost:9000")
s := sender.New(seq, tcp.New(conn))

// 7. Send messages — one call does everything
s.Send([]byte("hello"))
s.Send([]byte("world"))

// 8. Receive and validate incoming messages
for msg := range serverAdapter.Receive() {
    verdict := seq.Validate(msg.Seq)
    switch verdict {
    case session.Deliver:
        // deliver message, then drain any now-unblocked pending
        for _, s := range seq.FlushPending() {
            // deliver held payloads for these seqs
            _ = s
        }
    case session.DeliverPending:
        // hold payload — gap exists, waiting for earlier seq
    case session.DropDuplicate:
        // already delivered, discard
    case session.DropViolation:
        // beyond window, reject
    }
}

// 9. When connection drops, mark session disconnected
handler.Disconnect(sess.ID)

// --- On reconnect ---

// 10. Client sends RESUME with its signed token
result := handler.Resume(handshake.ResumeRequest{
    SessionID:         sess.ID,
    LastAckFromServer: lastAck,
    ResumeToken:       token, // HMAC-SHA256 signed — session ID alone is not enough
    RequestedAt:       time.Now(),
})

if result.Accepted {
    // retransmit anything the client missed
    for _, m := range result.Retransmit {
        newSender.Adapter().Send(transport.Message{Seq: m.Seq, Payload: m.Payload})
    }
    if result.Partial {
        // buffer rolled over — some messages unrecoverable
        // oldest recoverable seq is result.OldestRecoverable
    }
    // session is active again, sequence continues from result.ResumePoint
}
```

---

## Running the Tests

```bash
# all tests across all packages
go test ./... -v

# specific packages
go test ./session/... -v
go test ./handshake/... -v
go test ./transport/sender/... -v
go test ./transport/tcp/... -v
go test ./transport/websocket/... -v
go test ./store/memory/... -v
go test ./store/file/... -v
go test ./integration/... -v
```

Core packages have no external dependencies. The WebSocket adapter depends on `nhooyr.io/websocket` — isolated to `transport/websocket/` only.

---

## What This Is Not

- Not a replacement for TCP, QUIC, or any transport protocol
- Not a congestion control or flow control system
- Not an auth system — auth hooks plug in at the handshake boundary
- Not tied to any one language or deployment model

---

## Status

Core protocol is complete and tested:

- [x] Session state machine with lifecycle transitions
- [x] Monotonic message sequencing with sliding window deduplication
- [x] Reorder buffer — out-of-order messages held and delivered in sequence order
- [x] RESUME handshake with resume point negotiation
- [x] HMAC-SHA256 signed resume tokens with constant-time verification
- [x] Outbound ring buffer with retransmission on resume
- [x] Partial recovery signaling when buffer rolls over
- [x] TCP transport adapter with binary message framing
- [x] WebSocket transport adapter with JSON framing
- [x] Sender — atomic sequence assignment, delivery, and buffering in one call
- [x] In-memory session store
- [x] File-backed session store with atomic writes and restart persistence
- [x] Disconnect flushes file store — sequencer position durable before session goes dark
- [x] End-to-end integration tests covering all guarantees
- [x] Working example in `examples/basic/`
- [x] GitHub Actions CI
- [x] `FlushPending()` correctly returns seqs drained when a gap fills
- [x] `ResumeTo()` handles forward resume — no spurious rejection on server state lag
- [x] `Ack(seq)` trims outbound buffer mid-session — buffer size bounded without disconnect

---

## License

MIT