// Package applog writes the application log to a size-capped file.
package applog

import (
	"os"
	"path/filepath"
	"sync"
)

// File appends to path and, once it grows past max bytes, moves it to
// path+".1" (replacing the previous one) and starts afresh.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func Open(path string, max int64) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w := &File{path: path, max: max}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *File) open() error {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *File) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *File) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	w.f = nil
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return err
	}
	return w.open()
}

func (w *File) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
