package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"uuid"

	"github.com/pelletier/go-toml/v2"

	"github.com/gxd3v/quarry/internal/driver"
)

// Envs are the accepted connection environments, from safest to most
// dangerous. An empty Env means "local".
var Envs = []string{"local", "dev", "qa", "prod"}

type connectionsDoc struct {
	Connection []driver.Config `toml:"connection"`
}

// LoadConnections reads the connections file at path. A missing file yields
// no connections. Entries without an ID (or with a duplicated one) get a new
// one, which sticks once the file is saved again.
func LoadConnections(path string) ([]driver.Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []driver.Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc connectionsDoc
	if err := toml.Unmarshal(b, &doc); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, fmt.Errorf("%s:%d:%d: %w", path, row, col, err)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfgs := doc.Connection
	if cfgs == nil {
		cfgs = []driver.Config{}
	}
	seen := make(map[uuid.UUID]bool, len(cfgs))
	for i := range cfgs {
		if cfgs[i].ID == uuid.Nil() || seen[cfgs[i].ID] {
			cfgs[i].ID = uuid.NewV7()
		}
		seen[cfgs[i].ID] = true
	}
	return cfgs, nil
}

// SaveConnections atomically replaces the connections file at path with cfgs,
// in the given order. Entries with a nil ID are assigned one in place.
func SaveConnections(path string, cfgs []driver.Config) (err error) {
	for i := range cfgs {
		if cfgs[i].ID == uuid.Nil() {
			cfgs[i].ID = uuid.NewV7()
		}
	}
	b, err := toml.Marshal(connectionsDoc{Connection: cfgs})
	if err != nil {
		return err
	}
	if len(cfgs) == 0 {
		b = nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// CreateTemp creates the file with mode 0600.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Validate checks cfg before it is saved. others are the existing connections;
// the entry with cfg's ID among them is ignored. Errors are meant to be shown
// to the user as is.
func Validate(cfg driver.Config, others []driver.Config) error {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		return errors.New("Name is required")
	}
	for _, o := range others {
		if o.ID != cfg.ID && strings.EqualFold(strings.TrimSpace(o.Name), name) {
			return fmt.Errorf("A connection named %q already exists", name)
		}
	}
	switch cfg.Kind {
	case driver.Postgres, driver.Oracle, driver.MongoDB, driver.Redis:
	case "":
		return errors.New("Database type is required")
	default:
		return fmt.Errorf("Unknown database type %q", cfg.Kind)
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return errors.New("Port must be between 1 and 65535")
	}
	if cfg.Env != "" && !slices.Contains(Envs, cfg.Env) {
		return fmt.Errorf("Environment must be one of %s", strings.Join(Envs, ", "))
	}
	return nil
}

// DefaultPort is the port used when a connection leaves it at 0.
func DefaultPort(k driver.Kind) int {
	switch k {
	case driver.Postgres:
		return 5432
	case driver.Oracle:
		return 1521
	case driver.MongoDB:
		return 27017
	case driver.Redis:
		return 6379
	}
	return 0
}
