package session

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// SessionState represents what state a session is currently in.
// We use iota to auto-assign integer values to each constant.
type SessionState int

const (
	StateConnecting   SessionState = iota // 0 - initial, transport not yet ready
	StateActive                           // 1 - fully live, messages flowing
	StateDisconnected                     // 2 - transport dropped, waiting for resume
	StateResuming                         // 3 - client reconnected, handshake in progress
	StateExpired                          // 4 - TTL exceeded or explicitly closed, terminal
)

// Policy defines how long a session is allowed to live.
// We keep it simple for now — just a name and a max duration.
type Policy struct {
	Name        string
	MaxLifetime time.Duration
}

// These are the four built-in policies from our design.
// Applications pick one. Custom policies come later.
var (
	Ephemeral   = Policy{Name: "ephemeral", MaxLifetime: 30 * time.Second}
	Interactive = Policy{Name: "interactive", MaxLifetime: 5 * time.Minute}
	Durable     = Policy{Name: "durable", MaxLifetime: 2 * time.Hour}
)

// Session is the core struct. One of these exists for every active session.
// It holds identity, state, message tracking, and lifecycle info.
type Session struct {
	ID               string       // cryptographically random, stable across reconnects
	State            SessionState // current state in the lifecycle
	LastDeliveredSeq uint64       // highest sequence number we've delivered, never resets
	CreatedAt        time.Time    // when this session was first created
	LastActiveAt     time.Time    // last time we saw activity, used for TTL enforcement
	Policy           Policy       // which lifetime policy this session runs under
	ReconnectCount   int          // how many times this session has resumed, for observability
}

// NewSession creates a fresh session with a random ID and sets initial state.
// This is the only way to create a session — no raw struct literals.
func NewSession(policy Policy) (*Session, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	return &Session{
		ID:               id,
		State:            StateConnecting,
		LastDeliveredSeq: 0,
		CreatedAt:        now,
		LastActiveAt:     now,
		Policy:           policy,
		ReconnectCount:   0,
	}, nil
}

// IsExpired checks whether this session has exceeded its policy lifetime.
// Called before any operation to gate-keep expired sessions.
func (s *Session) IsExpired() bool {
	return time.Since(s.CreatedAt) > s.Policy.MaxLifetime
}

// Transition moves the session to a new state.
// Not all transitions are valid — this enforces the rules.
func (s *Session) Transition(next SessionState) bool {
	if !isValidTransition(s.State, next) {
		return false
	}
	s.State = next
	s.LastActiveAt = time.Now()
	return true
}

// isValidTransition defines which state changes are legal.
// Expired is terminal — nothing can come after it.
func isValidTransition(from, to SessionState) bool {
	allowed := map[SessionState][]SessionState{
		StateConnecting:   {StateActive, StateExpired},
		StateActive:       {StateDisconnected, StateExpired},
		StateDisconnected: {StateResuming, StateExpired},
		StateResuming:     {StateActive, StateExpired},
		StateExpired:      {}, // terminal, no exits
	}

	for _, valid := range allowed[from] {
		if to == valid {
			return true
		}
	}
	return false
}

// generateID creates a cryptographically random 32-character hex session ID.
// This is what makes session identity stable and unguessable.
func generateID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
