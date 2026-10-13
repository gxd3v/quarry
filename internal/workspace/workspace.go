// Package workspace persists the user's editing state: script files and their
// connection bindings, scratch files, session restore and query history.
package workspace

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	scratchDirName = "scratches"
	historyDirName = "history"
	bindingsName   = "bindings.json"
	sessionName    = "session.json"
	scratchSeqName = "scratch.seq"

	maxHistoryFile = 5 << 20
)

// caseInsensitive reports whether paths differ only by case refer to the same
// file. A variable so that tests can exercise both behaviours.
var caseInsensitive = runtime.GOOS == "windows"

// jsonOpts make marshaling never fail on text that is not valid UTF-8 (such
// as a Latin-1 script); invalid bytes become U+FFFD.
var jsonOpts = jsontext.AllowInvalidUTF8(true)

// Store is the workspace state kept in the app's state directory. It is safe
// for concurrent use.
type Store struct {
	dir, scratchDir, historyDir string
	maxHistory                  int64

	mu       sync.Mutex // guards bindings and scratch numbering
	bindings map[string]Binding

	sessMu      sync.Mutex
	lastSession []byte            // as last read or written
	encoded     map[string][]byte // tab content -> its JSON string

	histMu sync.RWMutex
	endsOK map[string]bool // history files known to end with a newline
}

// Open opens the workspace store rooted at stateDir (normally
// store.StateDir()), creating it and its subdirectories as needed.
func Open(stateDir string) (*Store, error) {
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	s := &Store{
		dir:        dir,
		scratchDir: filepath.Join(dir, scratchDirName),
		historyDir: filepath.Join(dir, historyDirName),
		maxHistory: maxHistoryFile,
		endsOK:     map[string]bool{},
	}
	for _, d := range []string{s.dir, s.scratchDir, s.historyDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	if err := s.loadBindings(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir is the store's state directory.
func (s *Store) Dir() string { return s.dir }

// pathKey is the normalized form of path used to compare and index paths.
func pathKey(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if caseInsensitive {
		abs = strings.ToLower(abs)
	}
	return abs, nil
}

// readJSON decodes the JSON file at path into v. A missing file leaves v
// untouched; a corrupt one is moved aside and v reset to its zero value, so
// that bad state never prevents the app from starting.
func readJSON[T any](path string, v *T) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, v, jsonOpts); err != nil {
		var zero T
		*v = zero
		return nil, quarantine(path)
	}
	return b, nil
}

// quarantine renames a corrupt file to <path>.corrupt-<timestamp>.
func quarantine(path string) error {
	dst := fmt.Sprintf("%s.corrupt-%s", path, time.Now().Format("20060102-150405.000"))
	if err := rename(path, dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeAtomic replaces path with data via a temporary file in the same
// directory and a rename, so readers see either the old or the new content.
// fsync is optional: it costs milliseconds and is not needed for state that
// can be rebuilt.
func writeAtomic(path string, data []byte, perm fs.FileMode, sync bool) (err error) {
	// CreateTemp creates the file with mode 0600.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if sync {
		if err = f.Sync(); err != nil {
			return err
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	if perm != 0o600 {
		if err = os.Chmod(f.Name(), perm); err != nil {
			return err
		}
	}
	return rename(f.Name(), path)
}

// rename is os.Rename, retried briefly on Windows where antivirus and indexers
// hold files open for short periods.
func rename(oldpath, newpath string) error {
	err := os.Rename(oldpath, newpath)
	for i := 1; err != nil && runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) && i <= 5; i++ {
		time.Sleep(time.Duration(i) * 10 * time.Millisecond)
		err = os.Rename(oldpath, newpath)
	}
	return err
}
