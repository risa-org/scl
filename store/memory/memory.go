package memory

import (
	"sync"

	"github.com/risa-org/scl/session"
)

// Store is a thread-safe in-memory implementation of handshake.SessionStore.
// Suitable for single-process servers and testing.
// Not suitable for multi-process deployments — sessions are lost on restart.
type Store struct {
	mu         sync.RWMutex
	sessions   map[string]*session.Session
	sequencers map[string]*session.Sequencer
}

// New creates an empty in-memory store.
func New() *Store {
	return &Store{
		sessions:   make(map[string]*session.Session),
		sequencers: make(map[string]*session.Sequencer),
	}
}

// Create makes a new session with the given policy and stores it.
// Returns the session, its sequencer, and any error.
func (s *Store) Create(policy session.Policy) (*session.Session, *session.Sequencer, error) {
	sess, err := session.NewSession(policy)
	if err != nil {
		return nil, nil, err
	}
	seq := session.NewSequencer()

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.sequencers[sess.ID] = seq
	s.mu.Unlock()

	return sess, seq, nil
}

// Get retrieves a session and its sequencer by ID.
// Returns false if the session does not exist.
// Satisfies the handshake.SessionStore interface.
func (s *Store) Get(sessionID string) (*session.Session, *session.Sequencer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil, false
	}
	return sess, s.sequencers[sessionID], true
}

// Delete removes a session from the store.
// Called when a session expires or is explicitly closed.
func (s *Store) Delete(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	delete(s.sequencers, sessionID)
	s.mu.Unlock()
}

// Count returns the number of sessions currently in the store.
// Useful for observability and testing.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}
