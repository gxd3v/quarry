package workspace

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	"github.com/gxd3v/quarry/internal/driver"
)

// Entry is one executed statement in the query history.
type Entry struct {
	At       time.Time
	Conn     uuid.UUID
	Scope    driver.Scope
	Text     string
	Duration time.Duration
	Rows     int64 // -1 when unknown
	Err      string
}

// entryJSON is the on-disk form of an Entry. Conn is implied by the file.
type entryJSON struct {
	At     time.Time `json:"at"`
	DB     string    `json:"db,omitempty"`
	Schema string    `json:"schema,omitempty"`
	Text   string    `json:"text"`
	Dur    int64     `json:"dur_ns,omitzero"`
	Rows   int64     `json:"rows"`
	Err    string    `json:"err,omitempty"`
}

func (s *Store) historyPath(conn uuid.UUID) string {
	return filepath.Join(s.historyDir, conn.String()+".jsonl")
}

// AppendHistory appends e to the history of e.Conn, stamping it with the
// current time if e.At is zero. When the file would grow past 5 MB it is
// rotated; only the previous file is kept.
func (s *Store) AppendHistory(e Entry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	line, err := json.Marshal(entryJSON{
		At:     e.At,
		DB:     e.Scope.Database,
		Schema: e.Scope.Schema,
		Text:   e.Text,
		Dur:    int64(e.Duration),
		Rows:   e.Rows,
		Err:    e.Err,
	}, jsonOpts)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.histMu.Lock()
	defer s.histMu.Unlock()
	path := s.historyPath(e.Conn)
	f, size, err := openHistory(path)
	if err != nil {
		return err
	}
	if size > 0 && size+int64(len(line)) > s.maxHistory {
		f.Close()
		if err := rename(path, path+".1"); err != nil {
			return err
		}
		if f, size, err = openHistory(path); err != nil {
			return err
		}
	}
	// Terminate a line left partial by a crash so that it does not swallow
	// this entry. Checked once per file: reading from the file being
	// appended to is slow on Windows.
	if size > 0 && !s.endsOK[path] && !endsWithNewline(path, size) {
		line = append([]byte{'\n'}, line...)
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	s.endsOK[path] = err == nil
	return err
}

func openHistory(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

func endsWithNewline(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	b := []byte{0}
	_, err = f.ReadAt(b, size-1)
	return err != nil || b[0] == '\n'
}

// History returns up to limit entries of conn's history, newest first, whose
// text contains query ignoring case. An empty query matches everything and a
// limit <= 0 means no limit. Corrupt lines are skipped. Files are read
// backwards, so a small limit only reads the tail of the history.
func (s *Store) History(conn uuid.UUID, query string, limit int) ([]Entry, error) {
	lq := strings.ToLower(query)
	// A matching text appears verbatim in its JSON line unless the query
	// contains characters JSON escapes, so lines can be screened before
	// decoding them.
	screen := lq != "" && !strings.ContainsFunc(lq, func(r rune) bool {
		return r < 0x20 || r == '"' || r == '\\' || r == 0xfffd
	})
	blq := []byte(lq)

	s.histMu.RLock()
	defer s.histMu.RUnlock()
	var out []Entry
	path := s.historyPath(conn)
	for _, p := range []string{path, path + ".1"} {
		done := false
		err := readLinesReverse(p, func(line []byte) bool {
			if screen && !bytes.Contains(bytes.ToLower(line), blq) {
				return true
			}
			var w entryJSON
			if json.Unmarshal(line, &w, jsonOpts) != nil {
				return true
			}
			if lq != "" && !strings.Contains(strings.ToLower(w.Text), lq) {
				return true
			}
			out = append(out, Entry{
				At:       w.At,
				Conn:     conn,
				Scope:    driver.Scope{Database: w.DB, Schema: w.Schema},
				Text:     w.Text,
				Duration: time.Duration(w.Dur),
				Rows:     w.Rows,
				Err:      w.Err,
			})
			done = limit > 0 && len(out) >= limit
			return !done
		})
		if err != nil {
			return out, err
		}
		if done {
			break
		}
	}
	return out, nil
}

// readLinesReverse calls fn with each non-empty line of the file at path,
// last line first, until fn returns false. A missing file has no lines. fn
// must not retain line.
func readLinesReverse(path string, fn func(line []byte) bool) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	const block = 64 << 10
	var carry []byte // start of the line that straddles the previous block
	for off := fi.Size(); off > 0; {
		n := min(block, off)
		off -= n
		chunk := make([]byte, int(n)+len(carry))
		if _, err := f.ReadAt(chunk[:n], off); err != nil {
			return err
		}
		copy(chunk[n:], carry)
		end := len(chunk)
		for {
			i := bytes.LastIndexByte(chunk[:end], '\n')
			if i < 0 {
				break
			}
			if i+1 < end && !fn(chunk[i+1:end]) {
				return nil
			}
			end = i
		}
		carry = chunk[:end]
	}
	if len(carry) > 0 {
		fn(carry)
	}
	return nil
}

// ClearHistory deletes conn's history, including the rotated file.
func (s *Store) ClearHistory(conn uuid.UUID) error {
	s.histMu.Lock()
	defer s.histMu.Unlock()
	path := s.historyPath(conn)
	delete(s.endsOK, path)
	var errs []error
	for _, p := range []string{path, path + ".1"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
