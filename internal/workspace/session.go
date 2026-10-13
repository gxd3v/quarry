package workspace

import (
	"bytes"
	"encoding/json/v2"
	"path/filepath"
	"uuid"

	"github.com/gxd3v/quarry/internal/driver"
)

// Session is the editor layout restored at startup.
type Session struct {
	Tabs     []TabState `json:"tabs,omitempty"`
	Active   int        `json:"active,omitzero"`
	SideW    int        `json:"side_w,omitzero"`
	ResH     int        `json:"res_h,omitzero"`
	ShowSide bool       `json:"show_side,omitzero"`
	ShowRes  bool       `json:"show_res,omitzero"`
}

// TabState is one editor tab of a Session.
type TabState struct {
	Path    string       `json:"path,omitempty"` // file or scratch path; "" for untitled tabs
	Title   string       `json:"title,omitempty"`
	Conn    uuid.UUID    `json:"conn,omitzero"`
	Scope   driver.Scope `json:"scope,omitzero"`
	Cursor  int          `json:"cursor,omitzero"`
	Unsaved string       `json:"-"` // content of untitled or dirty tabs
	Dirty   bool         `json:"dirty,omitzero"`
}

// sessionFile is the on-disk session. Tab contents are kept apart from the
// layout so that their (costly) JSON encoding can be reused between saves.
type sessionFile struct {
	Session Session  `json:"session"`
	Unsaved []string `json:"unsaved"` // parallel to Session.Tabs
}

// LoadSession reads the saved session. A missing or corrupt session file
// yields a zero Session; a corrupt one is kept aside as session.json.corrupt-*.
func (s *Store) LoadSession() (Session, error) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	var f sessionFile
	b, err := readJSON(filepath.Join(s.dir, sessionName), &f)
	if err != nil {
		return Session{}, err
	}
	sess := f.Session
	for i := range min(len(sess.Tabs), len(f.Unsaved)) {
		sess.Tabs[i].Unsaved = f.Unsaved[i]
	}
	if sess.Active < 0 || sess.Active >= len(sess.Tabs) {
		sess.Active = 0
	}
	s.lastSession = b
	return sess, nil
}

// SaveSession atomically replaces the saved session. It is meant to be
// called often: tab contents unchanged since the previous save are not
// re-encoded, and a session identical to the last one saved or loaded is not
// written at all.
func (s *Store) SaveSession(sess Session) error {
	meta, err := json.Marshal(sess, jsonOpts)
	if err != nil {
		return err
	}
	s.sessMu.Lock()
	defer s.sessMu.Unlock()

	encoded := make(map[string][]byte, len(sess.Tabs))
	parts := make([][]byte, len(sess.Tabs))
	size := len(meta) + 32
	for i, t := range sess.Tabs {
		v, ok := encoded[t.Unsaved]
		if !ok {
			v, ok = s.encoded[t.Unsaved]
		}
		if !ok {
			if v, err = json.Marshal(t.Unsaved, jsonOpts); err != nil {
				return err
			}
		}
		encoded[t.Unsaved] = v
		parts[i] = v
		size += len(v) + 1
	}
	b := make([]byte, 0, size)
	b = append(b, `{"session":`...)
	b = append(b, meta...)
	b = append(b, `,"unsaved":[`...)
	for i, v := range parts {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, v...)
	}
	b = append(b, "]}\n"...)
	s.encoded = encoded

	if bytes.Equal(b, s.lastSession) {
		return nil
	}
	// No fsync: it would dominate the cost of frequent saves. Readers still
	// see the old or the new file; one damaged by a power loss is quarantined.
	if err := writeAtomic(filepath.Join(s.dir, sessionName), b, 0o600, false); err != nil {
		return err
	}
	s.lastSession = b
	return nil
}
