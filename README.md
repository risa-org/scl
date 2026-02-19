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
> Ordered, at-most-once message delivery across transient disconnects — within a bounded session lifetime.

---

## How It Works

```
Application
    ↑
Session Continuity Layer
    ↑
Transport Adapter (TCP / QUIC / WebSocket)
    ↑
Network
```

When a client reconnects it sends a RESUME request:

```
Client → Server:  RESUME { session_id, last_ack, resume_token }
Server → Client:  RESUME_OK { resume_point }
               or RESUME_REJECT { reason }
```

Resume point = `min(client_last_ack, server_last_delivered)`. Server is authoritative. Both sides resync from the last point they both agree on. Sequence numbers continue from where they left off — no gaps, no resets, no duplicates.

---

## Project Structure

```
scl/
├── session/          # Core: state machine, lifecycle, policy TTLs
├── handshake/        # RESUME protocol logic
├── transport/        # Adapter interface every transport must satisfy
│   └── tcp/          # TCP adapter with message framing
└── integration/      # End-to-end tests proving the full stack works
```

**Key rule:** `session/` and `handshake/` never import `transport/`. Core logic never knows about networking. Transport adapters depend on core, not the other way around.

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
// --- Server side ---

// 1. Create a session manager (in-memory, or back it with Redis)
manager := NewSessionManager()
handler := handshake.NewHandler(manager)

// 2. Create a session when client first connects
sess, seq, _ := manager.Create(session.Interactive)
sess.Transition(session.StateActive)

// 3. Wrap your TCP connection in an adapter
conn, _ := net.Dial("tcp", "localhost:9000")
adapter := tcp.New(conn)

// 4. Send messages — seq numbers assigned automatically
adapter.Send(transport.Message{
    Seq:     seq.Next(),
    Payload: []byte("hello"),
})

// 5. When connection drops, mark session disconnected
handler.Disconnect(sess.ID)

// --- On reconnect ---

// 6. Client sends RESUME
result := handler.Resume(handshake.ResumeRequest{
    SessionID:         sess.ID,
    LastAckFromServer: lastAck,
    ResumeToken:       sess.ID,
    RequestedAt:       time.Now(),
})

if result.Accepted {
    // session is active again, sequence continues from result.ResumePoint
}
```

---

## Running the Tests

```bash
# all tests across all packages
go test ./... -v

# specific package
go test ./session/... -v
go test ./handshake/... -v
go test ./transport/tcp/... -v
go test ./integration/... -v
```

No external dependencies. No network ports required — transport tests use `net.Pipe()`.

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
- [x] RESUME handshake with resume point negotiation
- [x] TCP transport adapter with message framing
- [x] End-to-end integration test proving disconnect and resume

In progress:

- [ ] Working example in `examples/basic/`
- [ ] WebSocket transport adapter
- [ ] Cryptographic resume token signing
- [ ] Session persistence beyond in-memory

---

## License

MIT