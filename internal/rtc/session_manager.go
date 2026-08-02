package rtc

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

var (
	ErrSessionExists   = errors.New("session already exists")
	ErrSessionNotFound = errors.New("session not found")
)

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*sessionEntry
	server   sessionFactory
	ttl      time.Duration
	logger   *slog.Logger
}

type sessionFactory interface {
	NewSession(context.Context, string, string, func()) (*Session, string, error)
}

type sessionEntry struct {
	session *Session
}

func NewManager(server sessionFactory, ttl time.Duration, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{sessions: make(map[string]*sessionEntry), server: server, ttl: ttl, logger: logger}
}

func (m *Manager) Create(ctx context.Context, id, offer string) (string, error) {
	m.mu.Lock()
	if _, exists := m.sessions[id]; exists {
		m.mu.Unlock()
		return "", ErrSessionExists
	}
	entry := &sessionEntry{}
	m.sessions[id] = entry
	m.mu.Unlock()

	session, answer, err := m.server.NewSession(ctx, id, offer, func() { m.removeEntry(id, entry) })
	if err != nil {
		m.removeEntry(id, entry)
		return "", err
	}
	m.mu.Lock()
	current, exists := m.sessions[id]
	if !exists || current != entry {
		m.mu.Unlock()
		_ = session.Close()
		return "", ErrSessionNotFound
	}
	entry.session = session
	m.mu.Unlock()
	if m.ttl > 0 {
		session.setExpiryTimer(time.AfterFunc(m.ttl, func() { m.expire(id, session) }))
	}
	return answer, nil
}

func (m *Manager) expire(id string, expected *Session) {
	m.mu.Lock()
	entry, exists := m.sessions[id]
	matches := exists && entry.session == expected
	if matches {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if matches {
		_ = expected.Close()
	}
}

func (m *Manager) removeEntry(id string, expected *sessionEntry) {
	m.mu.Lock()
	if current, exists := m.sessions[id]; exists && current == expected {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
}

func (m *Manager) MarkConnected(id string) error {
	m.mu.RLock()
	entry, exists := m.sessions[id]
	var session *Session
	if exists {
		session = entry.session
	}
	m.mu.RUnlock()
	if session == nil {
		return ErrSessionNotFound
	}
	session.MarkConnected()
	return nil
}

func (m *Manager) Close(id string) error {
	m.mu.Lock()
	entry, exists := m.sessions[id]
	var session *Session
	if exists {
		session = entry.session
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if session == nil {
		return ErrSessionNotFound
	}
	return session.Close()
}

func (m *Manager) CloseAll() error {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, entry := range m.sessions {
		if entry.session != nil {
			sessions = append(sessions, entry.session)
		}
	}
	clear(m.sessions)
	m.mu.Unlock()

	var result error
	for _, session := range sessions {
		result = errors.Join(result, session.Close())
	}
	return result
}

func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}
