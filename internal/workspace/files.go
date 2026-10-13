package workspace

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/gxd3v/quarry/internal/driver"
)

// MaxFileSize is the largest script ReadFile accepts.
const MaxFileSize = 10 << 20

var (
	ErrTooLarge = errors.New("file is larger than 10 MB")
	ErrBinary   = errors.New("file looks binary or UTF-16 (it contains NUL bytes)")
)

var bom = []byte("\xef\xbb\xbf")

// ReadFile reads a script as editor text: a UTF-8 BOM is dropped and CRLF line
// endings become LF. Files over MaxFileSize fail with ErrTooLarge and files
// containing NUL bytes (binaries, UTF-16) with ErrBinary.
func ReadFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	if fi.Size() > MaxFileSize {
		return "", fmt.Errorf("%s: %w (%.1f MB)", path, ErrTooLarge, float64(fi.Size())/(1<<20))
	}
	// The limit also guards against files that grow after Stat.
	b, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxFileSize {
		return "", fmt.Errorf("%s: %w", path, ErrTooLarge)
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("%s: %w", path, ErrBinary)
	}
	b = bytes.TrimPrefix(b, bom)
	if bytes.Contains(b, []byte("\r\n")) {
		b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	}
	return string(b), nil
}

// WriteFile atomically replaces the file at path with text, keeping the mode
// of an existing file (new files get 0644) and creating missing parent
// directories. If path is a symlink, its target is replaced.
func WriteFile(path, text string) error {
	perm := fs.FileMode(0o644)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory", path)
		}
		perm = fi.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, []byte(text), perm, true)
}

// Binding is the connection and scope a script runs against.
type Binding struct {
	Conn  uuid.UUID
	Scope driver.Scope
}

func (s *Store) loadBindings() error {
	m := map[string]Binding{}
	if _, err := readJSON(filepath.Join(s.dir, bindingsName), &m); err != nil {
		return err
	}
	if m == nil {
		m = map[string]Binding{}
	}
	s.bindings = m
	return nil
}

// Bind records that the script at path runs with b. A zero Binding removes
// the record. Paths are compared absolute and cleaned, ignoring case on
// Windows.
func (s *Store) Bind(path string, b Binding) error {
	key, err := pathKey(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, had := s.bindings[key]
	if b == (Binding{}) {
		if !had {
			return nil
		}
		delete(s.bindings, key)
	} else {
		if had && old == b {
			return nil
		}
		s.bindings[key] = b
	}
	data, err := json.Marshal(s.bindings, json.Deterministic(true), jsonOpts)
	if err == nil {
		err = writeAtomic(filepath.Join(s.dir, bindingsName), data, 0o600, true)
	}
	if err != nil {
		if had {
			s.bindings[key] = old
		} else {
			delete(s.bindings, key)
		}
	}
	return err
}

// Binding returns the binding recorded for the script at path.
func (s *Store) Binding(path string) (Binding, bool) {
	key, err := pathKey(path)
	if err != nil {
		return Binding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[key]
	return b, ok
}

// FileInfo describes a scratch file.
type FileInfo struct {
	Path, Name string
	Modified   time.Time
	Size       int64
}

// NewScratch creates an empty scratch file named scratch-<n><ext> and returns
// its path. n grows with every scratch and is never reused, even after
// scratches are deleted.
func (s *Store) NewScratch(ext string) (string, error) {
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if strings.ContainsAny(ext, `/\`) || strings.ContainsRune(ext, 0) {
		return "", fmt.Errorf("invalid scratch extension %q", ext)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	seqPath := filepath.Join(s.dir, scratchSeqName)
	n := 0
	if b, err := os.ReadFile(seqPath); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	entries, err := os.ReadDir(s.scratchDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if m, ok := scratchNum(e.Name()); ok && m > n {
			n = m
		}
	}
	for {
		n++
		path := filepath.Join(s.scratchDir, "scratch-"+strconv.Itoa(n)+ext)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if err := writeAtomic(seqPath, []byte(strconv.Itoa(n)+"\n"), 0o600, false); err != nil {
			return "", err
		}
		return path, nil
	}
}

// scratchNum parses n from a "scratch-<n>[.ext]" file name.
func scratchNum(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "scratch-")
	if !ok {
		return 0, false
	}
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	n, err := strconv.Atoi(rest[:i])
	return n, err == nil
}

// Scratches lists the scratch files, most recently modified first.
func (s *Store) Scratches() ([]FileInfo, error) {
	entries, err := os.ReadDir(s.scratchDir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // removed meanwhile
		}
		out = append(out, FileInfo{
			Path:     filepath.Join(s.scratchDir, e.Name()),
			Name:     e.Name(),
			Modified: fi.ModTime(),
			Size:     fi.Size(),
		})
	}
	slices.SortStableFunc(out, func(a, b FileInfo) int {
		if c := b.Modified.Compare(a.Modified); c != 0 {
			return c
		}
		na, _ := scratchNum(a.Name)
		nb, _ := scratchNum(b.Name)
		return nb - na
	})
	return out, nil
}

// IsScratch reports whether path is a file in the scratch directory.
func (s *Store) IsScratch(path string) bool {
	key, err := pathKey(path)
	if err != nil {
		return false
	}
	dir, _ := pathKey(s.scratchDir)
	return filepath.Dir(key) == dir
}
