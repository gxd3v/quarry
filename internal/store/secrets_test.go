package store

import (
	"errors"
	"sync"
	"testing"
	"uuid"

	"github.com/zalando/go-keyring"
)

func testSecrets(t *testing.T, s Secrets) {
	t.Helper()
	id := uuid.NewV7()
	if _, err := s.Get(id); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("Get unknown: %v, want ErrNoSecret", err)
	}
	if err := s.Delete(id); err != nil {
		t.Fatalf("Delete unknown: %v", err)
	}
	if err := s.Set(id, "p@ss"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(id); err != nil || got != "p@ss" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Set(id, "new"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(id); got != "new" {
		t.Fatalf("Get after overwrite = %q", got)
	}
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(id); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("Get deleted: %v, want ErrNoSecret", err)
	}
}

func TestMemorySecrets(t *testing.T) {
	testSecrets(t, NewMemory())
}

func TestMemorySecretsConcurrent(t *testing.T) {
	s := NewMemory()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			id := uuid.NewV7()
			for range 100 {
				s.Set(id, "x")
				s.Get(id)
				s.Delete(id)
			}
		})
	}
	wg.Wait()
}

// fakeKeyring mimics go-keyring's behaviour without touching the OS.
type fakeKeyring struct {
	m   map[string]string
	err error
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.m[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Set(service, user, pw string) error {
	if f.err != nil {
		return f.err
	}
	f.m[service+"/"+user] = pw
	return nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.m[service+"/"+user]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.m, service+"/"+user)
	return nil
}

func TestKeyringSecrets(t *testing.T) {
	fk := &fakeKeyring{m: map[string]string{}}
	testSecrets(t, keyringSecrets{fk})

	id := uuid.NewV7()
	keyringSecrets{fk}.Set(id, "x")
	if _, ok := fk.m["quarry/"+id.String()]; !ok {
		t.Fatalf("secret not stored under service quarry and user %s: %v", id, fk.m)
	}
}

func TestKeyringUnavailable(t *testing.T) {
	cause := errors.New("org.freedesktop.DBus.Error.ServiceUnknown")
	s := keyringSecrets{&fakeKeyring{err: cause}}
	id := uuid.NewV7()
	_, getErr := s.Get(id)
	for name, err := range map[string]error{
		"get":    getErr,
		"set":    s.Set(id, "x"),
		"delete": s.Delete(id),
	} {
		if !errors.Is(err, ErrKeyringUnavailable) || !errors.Is(err, cause) {
			t.Errorf("%s: got %v, want ErrKeyringUnavailable wrapping the cause", name, err)
		}
	}

	s = keyringSecrets{&fakeKeyring{err: keyring.ErrSetDataTooBig}}
	if err := s.Set(id, "x"); !errors.Is(err, keyring.ErrSetDataTooBig) {
		t.Errorf("set too big: %v", err)
	}
}
