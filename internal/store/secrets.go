package store

import (
	"errors"
	"fmt"
	"sync"
	"uuid"

	"github.com/zalando/go-keyring"
)

// Secrets stores connection passwords keyed by connection ID.
type Secrets interface {
	// Get returns ErrNoSecret when nothing is stored for id.
	Get(id uuid.UUID) (string, error)
	Set(id uuid.UUID, secret string) error
	// Delete succeeds when nothing is stored for id.
	Delete(id uuid.UUID) error
}

var (
	ErrNoSecret = errors.New("no saved password")
	// ErrKeyringUnavailable means the OS keyring cannot be used (e.g. no
	// Secret Service on a headless Linux box); callers should fall back to
	// prompting, or to NewMemory for the session.
	ErrKeyringUnavailable = errors.New("OS keyring unavailable")
)

const keyringService = appName

// keyringBackend is the subset of go-keyring used here; tests swap it.
type keyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (osKeyring) Set(service, user, pw string) error       { return keyring.Set(service, user, pw) }
func (osKeyring) Delete(service, user string) error        { return keyring.Delete(service, user) }

type keyringSecrets struct{ kr keyringBackend }

// Keyring returns Secrets backed by the OS keyring (Secret Service, macOS
// Keychain, Windows Credential Manager).
func Keyring() Secrets { return keyringSecrets{osKeyring{}} }

func (k keyringSecrets) Get(id uuid.UUID) (string, error) {
	s, err := k.kr.Get(keyringService, id.String())
	if err != nil {
		return "", keyringErr(err)
	}
	return s, nil
}

func (k keyringSecrets) Set(id uuid.UUID, secret string) error {
	return keyringErr(k.kr.Set(keyringService, id.String(), secret))
}

func (k keyringSecrets) Delete(id uuid.UUID) error {
	err := keyringErr(k.kr.Delete(keyringService, id.String()))
	if errors.Is(err, ErrNoSecret) {
		return nil
	}
	return err
}

func keyringErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return ErrNoSecret
	default:
		return fmt.Errorf("%w: %w", ErrKeyringUnavailable, err)
	}
}

type memorySecrets struct {
	mu sync.Mutex
	m  map[uuid.UUID]string
}

// NewMemory returns Secrets kept in memory only, safe for concurrent use.
func NewMemory() Secrets { return &memorySecrets{m: make(map[uuid.UUID]string)} }

func (s *memorySecrets) Get(id uuid.UUID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	if !ok {
		return "", ErrNoSecret
	}
	return v, nil
}

func (s *memorySecrets) Set(id uuid.UUID, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = secret
	return nil
}

func (s *memorySecrets) Delete(id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}
