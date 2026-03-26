package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SessionState holds the full state for a single session mapping.
type SessionState struct {
	SessionID       string `json:"sessionId"`
	UpdatedAt       int64  `json:"updatedAt"`                 // Unix milliseconds
	TotalTokens     int    `json:"totalTokens,omitempty"`     // for stage 3
	ContextWindow   int    `json:"contextWindow,omitempty"`   // for stage 3
	CompactionCount int    `json:"compactionCount,omitempty"` // for stage 3
	MemoryFlushedAt int    `json:"memoryFlushedAt,omitempty"` // compactionCount snapshot at last flush
}

// Router maps channel-specific keys to session IDs with associated state.
type Router struct {
	path     string
	mu       sync.Mutex
	sessions map[string]SessionState
}

// nowMs returns the current time in Unix milliseconds.
// Package-level var so tests can override it.
var nowMs = func() int64 { return time.Now().UnixMilli() }

// New creates a Router backed by the given JSON file.
// It supports backward-compatible loading: the old format was map[string]string,
// the new format is map[string]SessionState.
func New(path string) (*Router, error) {
	r := &Router{
		path:     strings.TrimSpace(path),
		sessions: map[string]SessionState{},
	}
	if r.path == "" {
		return r, nil
	}

	data, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, fmt.Errorf("read session router: %w", err)
	}
	if len(data) == 0 {
		return r, nil
	}

	// Backward-compatible loading: try to detect old vs new format.
	// Unmarshal into map[string]json.RawMessage so we can inspect each value.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode session router: %w", err)
	}

	for k, v := range raw {
		trimmed := strings.TrimSpace(string(v))
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// Old format: plain string value
			var sid string
			if err := json.Unmarshal(v, &sid); err != nil {
				return nil, fmt.Errorf("decode session router entry %q: %w", k, err)
			}
			r.sessions[k] = SessionState{SessionID: sid, UpdatedAt: 0}
		} else {
			// New format: SessionState object
			var state SessionState
			if err := json.Unmarshal(v, &state); err != nil {
				return nil, fmt.Errorf("decode session router entry %q: %w", k, err)
			}
			r.sessions[k] = state
		}
	}

	return r, nil
}

// Resolve returns the session ID for key, or fallback if not found, or key itself.
func (r *Router) Resolve(key, fallback string) string {
	key = strings.TrimSpace(key)
	fallback = strings.TrimSpace(fallback)
	if r == nil {
		if fallback != "" {
			return fallback
		}
		return key
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.sessions[key]; ok && strings.TrimSpace(state.SessionID) != "" {
		return state.SessionID
	}
	if fallback != "" {
		return fallback
	}
	return key
}

// Current returns the current session ID for the given key.
func (r *Router) Current(key string) string {
	return r.Resolve(key, "")
}

// Rotate generates a new session ID for the given key and persists it.
func (r *Router) Rotate(key string) (oldSessionID, newSessionID string, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", errors.New("session key is empty")
	}
	if r == nil {
		newSessionID = nextSessionID(key)
		return key, newSessionID, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	oldState, ok := r.sessions[key]
	if ok && strings.TrimSpace(oldState.SessionID) != "" {
		oldSessionID = oldState.SessionID
	} else {
		oldSessionID = key
	}

	newSessionID = nextSessionID(key)
	r.sessions[key] = SessionState{
		SessionID: newSessionID,
		UpdatedAt: nowMs(),
	}
	if err := r.persistLocked(); err != nil {
		return "", "", err
	}
	return oldSessionID, newSessionID, nil
}

// Set explicitly sets the session ID for the given key.
func (r *Router) Set(key, sessionID string) error {
	key = strings.TrimSpace(key)
	sessionID = strings.TrimSpace(sessionID)
	if key == "" || r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if sessionID == "" || sessionID == key {
		delete(r.sessions, key)
	} else {
		r.sessions[key] = SessionState{
			SessionID: sessionID,
			UpdatedAt: nowMs(),
		}
	}
	return r.persistLocked()
}

// Reset deletes the session mapping for the given key.
func (r *Router) Reset(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, key)
	return r.persistLocked()
}

// Touch updates the UpdatedAt timestamp for the given key.
// If the key does not exist, it creates an entry with SessionID = key.
func (r *Router) Touch(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.sessions[key]
	if !ok {
		state = SessionState{SessionID: key}
	}
	state.UpdatedAt = nowMs()
	r.sessions[key] = state
	return r.persistLocked()
}

// ResolveWithState returns the session ID and full state for the given key.
func (r *Router) ResolveWithState(key string) (sessionID string, state SessionState, exists bool) {
	key = strings.TrimSpace(key)
	if r == nil {
		return key, SessionState{}, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, exists = r.sessions[key]
	if exists && strings.TrimSpace(state.SessionID) != "" {
		return state.SessionID, state, true
	}
	return key, SessionState{}, false
}

// CheckAndRotateIfStale evaluates the freshness of the session for the given key
// using the provided policy. If stale, it rotates to a new session.
func (r *Router) CheckAndRotateIfStale(key string, policy ResetPolicy) (sessionID string, rotated bool, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, errors.New("session key is empty")
	}
	if r == nil {
		return key, false, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.sessions[key]
	if !ok {
		// No existing session – create one, no rotation needed.
		newID := nextSessionID(key)
		r.sessions[key] = SessionState{
			SessionID: newID,
			UpdatedAt: nowMs(),
		}
		if err := r.persistLocked(); err != nil {
			return "", false, err
		}
		return newID, false, nil
	}

	now := nowMs()
	freshness := EvaluateFreshness(state.UpdatedAt, now, policy)

	if freshness == Stale {
		newID := nextSessionID(key)
		r.sessions[key] = SessionState{
			SessionID: newID,
			UpdatedAt: now,
		}
		if err := r.persistLocked(); err != nil {
			return "", false, err
		}
		return newID, true, nil
	}

	// Fresh – touch UpdatedAt and return current session.
	state.UpdatedAt = now
	r.sessions[key] = state
	if err := r.persistLocked(); err != nil {
		return "", false, err
	}
	return state.SessionID, false, nil
}

// UpdateUsage updates the token tracking fields for the given key.
func (r *Router) UpdateUsage(key string, totalTokens, contextWindow int) error {
	key = strings.TrimSpace(key)
	if key == "" || r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.sessions[key]
	if !ok {
		return nil // nothing to update
	}
	state.TotalTokens = totalTokens
	state.ContextWindow = contextWindow
	r.sessions[key] = state
	return r.persistLocked()
}

// MarkMemoryFlushed records that a memory flush was performed for the current compaction cycle.
func (r *Router) MarkMemoryFlushed(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.sessions[key]
	if !ok {
		return nil
	}
	state.MemoryFlushedAt = state.CompactionCount
	r.sessions[key] = state
	return r.persistLocked()
}

func (r *Router) persistLocked() error {
	if r == nil || r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("mkdir session router dir: %w", err)
	}
	data, err := json.MarshalIndent(r.sessions, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session router: %w", err)
	}
	if err := os.WriteFile(r.path, data, 0o600); err != nil {
		return fmt.Errorf("write session router: %w", err)
	}
	return nil
}

func nextSessionID(base string) string {
	return fmt.Sprintf("%s#%d", base, time.Now().UnixNano())
}
