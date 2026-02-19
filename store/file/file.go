package file

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/risa-org/scl/session"
)

// record is the JSON structure persisted to disk for each session.
// We store enough to reconstruct both the Session and Sequencer.
type record struct {
	ID                string               `json:"id"`
	State             session.SessionState `json:"state"`
	PolicyName        string               `json:"policy_name"`
	PolicyMaxLifetime time.Duration        `json:"policy_max_lifetime"`
	LastDeliveredSeq  uint64               `json:"last_delivered_seq"`
	CreatedAt         time.Time            `json:"created_at"`
	LastActiveAt      time.Time            `json:"last_active_at"`
	ReconnectCount    int                  `json:"reconnect_count"`
}

// Store is a file-backed implementation of handshake.SessionStore.
// Sessions are persisted to a JSON file and survive server restarts.
// Not suitable for multi-process deployments — use Redis or Postgres for that.
type Store struct {
	mu       sync.RWMutex
	path     string
	sessions map[string]*session.Session
	seqs     map[string]*session.Sequencer
}

// New creates a file-backed store at the given path.
// If the file exists, sessions are loaded from it on startup.
// If it doesn't exist, it will be created on first write.
func New(path string) (*Store, error) {
	s := &Store{
		path:     path,
		sessions: make(map[string]*session.Session),
		seqs:     make(map[string]*session.Sequencer),
	}

	if err := s.load(); err != nil {
		return nil, fmt.Errorf("failed to load sessions from %s: %w", path, err)
	}

	return s, nil
}

// Create makes a new session, stores it in memory, and flushes to disk.
func (s *Store) Create(policy session.Policy) (*session.Session, *session.Sequencer, error) {
	sess, err := session.NewSession(policy)
	if err != nil {
		return nil, nil, err
	}
	seq := session.NewSequencer()

	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.seqs[sess.ID] = seq
	err = s.flush()
	s.mu.Unlock()

	if err != nil {
		return nil, nil, fmt.Errorf("failed to persist session: %w", err)
	}

	return sess, seq, nil
}

// Get retrieves a session and sequencer by ID from memory.
// Satisfies handshake.SessionStore.
func (s *Store) Get(sessionID string) (*session.Session, *session.Sequencer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil, false
	}
	return sess, s.seqs[sessionID], true
}

// Delete removes a session from memory and flushes to disk.
func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	delete(s.seqs, sessionID)
	err := s.flush()
	s.mu.Unlock()
	return err
}

// Count returns the number of sessions currently stored.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// load reads sessions from the JSON file into memory.
// Called once at startup. If the file doesn't exist, returns nil — empty store.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil // fresh start, no file yet
	}
	if err != nil {
		return err
	}

	var records []record
	if err := json.Unmarshal(data, &records); err != nil {
		return err
	}

	for _, r := range records {
		policy := session.Policy{
			Name:        r.PolicyName,
			MaxLifetime: r.PolicyMaxLifetime,
		}

		sess := &session.Session{
			ID:             r.ID,
			State:          r.State,
			Policy:         policy,
			CreatedAt:      r.CreatedAt,
			LastActiveAt:   r.LastActiveAt,
			ReconnectCount: r.ReconnectCount,
		}

		seq := session.NewSequencer()
		seq.RestoreLastDelivered(r.LastDeliveredSeq)

		s.sessions[sess.ID] = sess
		s.seqs[sess.ID] = seq
	}

	return nil
}

// flush writes the current in-memory state to the JSON file.
// Must be called with the write lock held.
func (s *Store) flush() error {
	records := make([]record, 0, len(s.sessions))

	for id, sess := range s.sessions {
		seq := s.seqs[id]
		records = append(records, record{
			ID:                id,
			State:             sess.State,
			PolicyName:        sess.Policy.Name,
			PolicyMaxLifetime: sess.Policy.MaxLifetime,
			LastDeliveredSeq:  seq.LastDelivered(),
			CreatedAt:         sess.CreatedAt,
			LastActiveAt:      sess.LastActiveAt,
			ReconnectCount:    sess.ReconnectCount,
		})
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}

	// write to a temp file then rename — atomic on most systems
	// prevents corrupt file if process crashes mid-write
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
