# SCL — Build Log

A running record of what was built, every decision taken, and why.
Updated after every meaningful stage of development.

---

## What This Project Is

A **Session Continuity Layer** — a thin, transport-agnostic library that decouples session identity from transport lifetime.

The core problem it solves:

> Every networked app that needs real-time or persistent connections hits the same wall — the network drops, the connection resets, and the app has to rebuild everything from scratch.

TCP, WebSockets, and most transports implicitly equate:
```
connection == identity == state
```

This project breaks that coupling:
```
transport lifetime ≠ session identity
```

A session survives across multiple underlying connections. The transport changes. The identity doesn't.

**The one guarantee this library makes:**
> Ordered, at-most-once message delivery across transient disconnects — within a bounded session lifetime.

---

## What This Is Not

- Not a replacement for TCP, QUIC, or any transport
- Not a congestion control system
- Not an auth system
- Not a relay or NAT traversal tool
- Not tied to any one language or ecosystem

---

## Language Decision

**Go** — chosen over Rust for pragmatic reasons:
- Already familiar from an existing project
- Go's goroutines and channels map naturally onto session lifecycle management
- Strong standard library, no heavy dependencies needed
- Rust is the plan for a future port — once the protocol is proven and Go intuition is solid, porting to Rust becomes a focused language learning exercise rather than two hard things at once

---

## Repository Structure

```
scl/
├── go.mod
├── README.md
├── BUILDLOG.md
│
├── session/                  ← core logic, no networking
│   ├── session.go            ← state machine + Session struct
│   ├── sequence.go           ← message sequencing + sliding window
│   ├── session_test.go
│   └── sequence_test.go
│
├── handshake/                ← RESUME protocol logic
│   ├── handshake.go
│   └── handshake_test.go
│
├── transport/                ← adapters live here
│   ├── adapter.go            ← the interface every adapter must satisfy
│   └── tcp/
│       └── tcp.go
│
├── policy/                   ← ephemeral, interactive, durable
│   └── policy.go
│
└── examples/
    └── basic/
        └── main.go
```

**Key structural rule:** `session/` and `handshake/` import nothing from `transport/`. Core logic never knows about networking. Transport adapters depend on core, never the other way around.

---

## Build Stages

---

### Stage 1 — Session State Machine
**File:** `session/session.go`
**Commit:** `feat: session state machine with lifecycle transitions and policy TTLs`
**Tests:** `session/session_test.go` — 6 tests, all passing

**What it does:**
Defines what a session is and what states it can be in. Pure logic, no networking.

**States:**
```
connecting → active → disconnected → resuming → active (again)
                                              ↘ expired
```

**Key types:**
- `SessionState` — iota constants for each state
- `Policy` — named lifetime policy with a max duration
- `Session` — the core struct: ID, state, lastDeliveredSeq, createdAt, lastActiveAt, policy, reconnectCount

**Built-in policies:**
| Policy | Lifetime |
|--------|----------|
| Ephemeral | 30 seconds |
| Interactive | 5 minutes |
| Durable | 2 hours |

**Key decisions:**

- `crypto/rand` for session IDs, not `math/rand` — IDs must be unguessable. math/rand is deterministic and predictable.
- `Transition()` returns bool — illegal transitions (e.g. expired → active) are silently rejected. The state machine enforces rules, callers don't need to.
- `Expired` is terminal — no exits from expired, encoded directly in the transition map.
- `isValidTransition` uses a map of allowed transitions — easy to read, easy to audit, easy to change.
- `NewSession()` is the only way to create a session — no raw struct literals allowed, enforces correct initial state.

**Tests cover:**
- Fresh session has correct initial values
- Two sessions never share an ID
- Full happy-path lifecycle transitions
- Illegal transitions are rejected
- Policy TTL expiry works correctly
- Reconnect count can be tracked

---

### Stage 2 — Message Sequencer + Sliding Window
**File:** `session/sequence.go`
**Commit:** `feat: message sequencer with sliding window deduplication`
**Tests:** `session/sequence_test.go` — 9 tests, all passing

**What it does:**
Assigns monotonic sequence numbers to outgoing messages and validates incoming messages against a sliding window to enforce at-most-once delivery.

**The three rules:**
- `seq ≤ lastDelivered` → duplicate, drop it
- `seq > lastDelivered + windowSize` → protocol violation, reject
- anything in between → deliver it, advance the window

**Default window size:** 64 messages

**Key types:**
- `Sequencer` — owns nextSeq, lastDelivered, windowSize
- `DeliveryVerdict` — named iota: Deliver, DropDuplicate, DropViolation

**Key decisions:**

- Sequence numbers start at 1 not 0 — lets `lastDelivered = 0` unambiguously mean "nothing delivered yet". Starting at 0 would make the initial state ambiguous.
- `Validate()` advances `lastDelivered` itself — validation and recording delivery are atomic. You cannot validate without recording, which prevents a whole class of subtle bugs.
- `ResumeTo()` validates the resume point — you cannot resume to a point ahead of messages not yet sent. Guards against corrupt or malicious RESUME requests.
- `DeliveryVerdict` as a named type — `if verdict == Deliver` reads like English. `if valid == true` does not.
- Window size is a constant not a magic number — `DefaultWindowSize = 64` is explicit and changeable.

**Bug caught during testing:**
`TestValidateWindowEdge` initially used one sequencer for two assertions. Delivering seq 64 advanced `lastDelivered` to 64, which moved the window forward — so seq 65 became valid instead of a violation. Fix: two separate sequencers, one per assertion. Lesson: when a test fails, ask "is the code wrong or is the test wrong?" before touching anything.

**Tests cover:**
- Sequence numbers start at 1
- Numbers are strictly monotonic across 100 iterations
- Valid messages are delivered and window advances
- Duplicate messages are dropped
- Old messages below the window are dropped
- Messages beyond the window are rejected as violations
- Window edge cases (exactly at limit vs one beyond)
- ResumeTo restores correct state
- ResumeTo rejects invalid resume points

---

### Stage 3 — RESUME Handshake
**File:** `handshake/handshake.go`
**Commit:** `feat: RESUME handshake with session lookup, token validation, and resume point negotiation`
**Tests:** `handshake/handshake_test.go` — 8 tests, all passing

**What it does:**
The first file where Session and Sequencer meet. Processes RESUME requests from
reconnecting clients and either restores the session or rejects it with a clear reason.

**The protocol flow:**
1. Look up session by ID
2. Check it exists and isn't expired
3. Check it's in Disconnected state — only disconnected sessions can be resumed
4. Validate the resume token — proves client owns the session
5. Compute resume point = `min(client_last_ack, server_last_delivered)`
6. Call ResumeTo() on the sequencer to restore window position
7. Transition session Disconnected → Resuming → Active
8. Return RESUME_OK or RESUME_REJECT with a named reason

**Key types:**
- `ResumeRequest` — what the client sends: session ID, last ack, token
- `ResumeResult` — what the handler returns: accepted bool, resume point, reason
- `SessionStore` — interface for session lookup, decouples handshake from storage
- `Handler` — processes RESUME and Disconnect calls

**Rejection reasons (named constants):**
| Reason | When |
|--------|------|
| session_not_found | ID doesn't exist in store |
| session_expired | TTL exceeded |
| invalid_state | Session not in Disconnected state |
| invalid_token | Token doesn't match |
| invalid_resume_point | ResumeTo() rejected the computed point |

**Key decisions:**

- `SessionStore` as an interface — handshake never imports a concrete store.
  Backed by an in-memory map now, swappable to Redis or anything else later
  without touching this file. This is dependency inversion.
- `ResumeToken` validated against `sess.ID` for now — intentionally simple placeholder.
  The structure (issue token, validate on resume) is correct. Cryptographic signing comes later.
- Two transitions (Disconnected → Resuming → Active) — Resuming is a real observable
  state. Auth hooks and resource checks will plug in between these two later.
  Skipping straight to Active would remove that insertion point.
- `Disconnect()` on the handler — transport adapters need a way to signal a drop.
  Lives here because the handshake owns session state transitions.
- `min()` defined explicitly — Go 1.21+ has a built-in but explicit definition
  keeps compatibility and makes the intent visible in the code.

**Bugs caught during testing:**

Bug 1 — `ResumeTo` guard was wrong.
Original guard: `if lastDelivered >= sq.nextSeq` — compared resume point against
outgoing sequence counter. But `nextSeq` only advances when `Next()` is called
(outgoing messages). In the test we only called `Validate()` (incoming messages),
so `nextSeq` stayed at 1 and the guard rejected valid resume points.
Fix: changed guard to `if lastDelivered > sq.lastDelivered` — you cannot resume
beyond what was actually delivered, regardless of what was sent.

Bug 2 — `TestResumeTo` in sequence_test.go was written for the old guard.
It called `Next()` three times then tried to resume to 2. With the new guard,
`lastDelivered` was still 0 so resuming to 2 was correctly rejected.
Fix: replaced `Next()` calls with `Validate()` calls to properly simulate
delivered messages before resuming.

Lesson reinforced: two test failures in a row were test bugs, not code bugs.
When a test fails, always ask which one is wrong before touching anything.

---

### Stage 4 — Transport Adapter Interface
**File:** `transport/adapter.go`
**Commit:** `feat: transport adapter interface with message, disconnect reason, and event types`
**Tests:** `transport/adapter_test.go` — 3 tests, all passing

**What it does:**
Defines the contract every transport must satisfy. The session layer only ever
talks to this interface — it never imports tcp, quic, websocket, or anything
concrete. This is how "same core logic, swappable backends" actually works in code.

**Key types:**
- `Message` — seq + payload, moved together as one unit, transport never interprets either
- `DisconnectReason` — named constants: Unknown, NetworkError, Timeout, ClosedClean
- `DisconnectEvent` — bundles reason + error together for observability
- `Adapter` — the interface: Send, Receive, Disconnected, Close

**The four methods and why each exists:**
| Method | Returns | Purpose |
|--------|---------|---------|
| `Send(msg)` | error | Deliver a message to remote side |
| `Receive()` | `<-chan Message` | Stream of incoming messages |
| `Disconnected()` | `<-chan DisconnectEvent` | Signals when transport closes and why |
| `Close()` | error | Graceful shutdown, idempotent |

**Key decisions:**

- `Receive()` and `Disconnected()` return channels not callbacks — fits naturally
  into Go's select statement. A callback would require locking and goroutine
  coordination. A channel is idiomatic Go.
- `DisconnectReason` as named constants not strings — callers check with
  `reason == ReasonNetworkError` not string comparison. String comparison
  is fragile and breaks silently on typos.
- `Close()` is idempotent — in real networked code, close gets called from
  multiple places: timeout handlers, error handlers, shutdown signals.
  Making it safe to call multiple times means callers never coordinate who closes first.
- `ErrTransportClosed` as a named sentinel error — callers use `errors.Is()`
  to check the exact cause. Raw string errors are undetectable programmatically.
- Exactly four methods — no more. Every method has one job. If a fifth method
  feels necessary, question it hard first.

---

### Stage 5 — TCP Transport Adapter
**File:** `transport/tcp/tcp.go`
**Commit:** `feat: TCP transport adapter with message framing, read loop, and disconnect signaling`
**Tests:** `transport/tcp/tcp_test.go` — 5 tests, all passing. First networking code in the project.

**What it does:**
Implements the Adapter interface over a raw TCP connection. First code that
touches actual sockets. Handles message framing, concurrent reads and writes,
clean shutdown, and disconnect signaling.

**Wire format:**
```
[8 bytes: Seq uint64 big-endian][4 bytes: payload length uint32][N bytes: payload]
```
TCP is a stream protocol with no message boundaries. Without explicit framing,
a Read() call can return half a message or two joined together. This format
lets us always reconstruct exactly one message at a time.

**Key decisions:**

- `io.ReadFull` not `conn.Read` — Read() on TCP can return fewer bytes than
  asked even without error. ReadFull keeps reading until it has exactly what
  you asked for or the connection dies. Always use this for exact byte reads.
- `writeMu sync.Mutex` — TCP connections are not safe for concurrent writes.
  Without this lock two goroutines calling Send() simultaneously would
  interleave bytes on the wire and corrupt both messages.
- `sync.Once` for Close() — close gets called from the readLoop defer AND
  from external callers. Without Once you'd close twice and get an error.
- Buffered channels — incoming buffered at 64, disconnect at 1. Without
  buffering the readLoop blocks every time the caller is slow. 64 gives
  breathing room. Disconnect only ever fires once so 1 is enough.
- `net.Pipe()` in tests — gives an in-memory TCP-like connection with no
  real ports needed. Tests run fast, work offline, no cleanup required.

**No bugs caught — first try green.**

---

### Stage 6 — Integration Test
**File:** `integration/integration_test.go`
**Commit:** `feat: end-to-end integration test proving RESUME across real TCP disconnect`
**Tests:** 3 tests, all passing first try. First test to exercise the full stack.

**What it does:**
Wires all layers together into real end-to-end scenarios. No mocks, no fakes
for the transport — real TCP via net.Pipe(), real session state, real sequencer,
real handshake. Proves the entire design works as a system.

**Also introduced: SessionManager**
Concrete in-memory implementation of handshake.SessionStore. Ties together
session creation, sequencer management, and lookup under a RWMutex.
This is the runtime glue between the handshake and the session layer.

**Three scenarios tested:**

1. `TestFullSessionLifecycle` — happy path. Connect, send 3 messages,
   validate delivery, disconnect cleanly. Verifies the basic flow works.

2. `TestResumeAfterDisconnect` — the core guarantee. Send 5 messages,
   force disconnect, reconnect with RESUME, send 3 more. Proves sequence
   numbers are continuous: lastDelivered = 8 (5 + 3), no gaps, no resets.
   This is what the entire project exists to do.

3. `TestExpiredSessionResumeRejected` — TTL enforcement. Backdates session
   creation by 10 minutes, attempts RESUME, verifies rejection with
   correct reason code. Expired sessions cannot be resumed, ever.

**No bugs. Clean first run.**

---

### Stage 7 — README
**File:** `README.md`
**Commit:** `docs: add README with problem statement, architecture, and usage example`

Covers: what it is, why it exists, how it works, project structure, session policies, quick usage example, how to run tests, what it is not, and current status checklist.

---

### Stage 8 — Basic Example
**File:** `examples/basic/main.go`
**Commit:** `feat: basic example showing full connect, disconnect, and resume flow`

Runnable program demonstrating the complete flow with terminal output at
every step. Sequence numbers visible: 1-4 pre-disconnect, 5-7 post-resume.
Continuous across the boundary — exactly what the project promises.

---

### Stage 9 — HMAC Resume Token Signing
**Issue:** #1
**Branch:** `fix/issue-1-hmac-resume-token`
**File:** `session/token.go`
**Commit:** `fix: replace plaintext token with HMAC-SHA256 signed resume tokens (closes #1)`
**Tests:** 6 new token tests + 1 new integration test (TestForgedTokenRejected), all passing

**What changed:**
- `session/token.go` — TokenIssuer with Issue() and Verify()
- `handshake/handshake.go` — Handler now takes a TokenIssuer, uses Verify() instead of ==
- `integration/integration_test.go` — uses real tokens, added TestForgedTokenRejected
- `examples/basic/main.go` — uses NewRandomTokenIssuer, shows token in output

**Security improvement:**
Before: token == session ID. Anyone who observed traffic could forge a RESUME.
After: token = HMAC-SHA256(secret, session_id). Secret never leaves the server.
Knowing the session ID alone is not enough to hijack a session.
Constant-time comparison prevents timing attacks.

---

## Core Principles (Running List)

- Core logic never imports transport packages
- Tests are written immediately after each file, not later
- Commit after each meaningful, self-contained stage
- Every decision gets a reason — no unexplained choices
- Sequence numbers never reset, ever
- Sessions are not promises — rejection on reconnect is valid
- Observability is first class — reconnect count, session age, last disconnect reason