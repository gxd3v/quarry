// Package workspace serves the WorkspaceService: script files and their
// bindings, scratches, the restored session, query history and settings.
package workspace

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"uuid"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
	"github.com/gxd3v/quarry/gen/quarry/v1/quarryv1connect"
	"github.com/gxd3v/quarry/internal/driver"
	ws "github.com/gxd3v/quarry/internal/workspace"
	"github.com/gxd3v/quarry/service"
)

const defaultHistoryLimit = 200

// Service serves the WorkspaceService on top of a workspace store and a
// settings file.
type Service struct {
	ws           *ws.Store
	settingsPath string
	settingsMu   sync.Mutex
}

var _ quarryv1connect.WorkspaceServiceHandler = (*Service)(nil)

// New returns a Service storing settings as a JSON object at settingsPath.
func New(store *ws.Store, settingsPath string) *Service {
	return &Service{ws: store, settingsPath: settingsPath}
}

func (s *Service) ReadFile(_ context.Context, req *connect.Request[quarryv1.ReadFileRequest]) (*connect.Response[quarryv1.ReadFileResponse], error) {
	path := req.Msg.Path
	if err := checkPath(path); err != nil {
		return nil, err
	}
	text, err := ws.ReadFile(path)
	if err != nil {
		return nil, fileError(err)
	}
	res := &quarryv1.ReadFileResponse{Text: text, Scratch: s.ws.IsScratch(path)}
	if b, ok := s.ws.Binding(path); ok {
		res.Binding = &quarryv1.Binding{ConnId: b.Conn.String(), Scope: scopeProto(b.Scope)}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) WriteFile(_ context.Context, req *connect.Request[quarryv1.WriteFileRequest]) (*connect.Response[quarryv1.WriteFileResponse], error) {
	if err := checkPath(req.Msg.Path); err != nil {
		return nil, err
	}
	if err := ws.WriteFile(req.Msg.Path, req.Msg.Text); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.WriteFileResponse{}), nil
}

// Bind binds a script to a connection; a missing binding or conn_id unbinds it.
func (s *Service) Bind(_ context.Context, req *connect.Request[quarryv1.BindRequest]) (*connect.Response[quarryv1.BindResponse], error) {
	if err := checkPath(req.Msg.Path); err != nil {
		return nil, err
	}
	var b ws.Binding
	if pb := req.Msg.Binding; pb != nil && pb.ConnId != "" {
		id, err := parseConn(pb.ConnId)
		if err != nil {
			return nil, err
		}
		b = ws.Binding{Conn: id, Scope: service.ScopeOf(pb.Scope)}
	}
	if err := s.ws.Bind(req.Msg.Path, b); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.BindResponse{}), nil
}

func (s *Service) NewScratch(_ context.Context, req *connect.Request[quarryv1.NewScratchRequest]) (*connect.Response[quarryv1.NewScratchResponse], error) {
	ext := req.Msg.Ext
	if ext == "" {
		ext = ".sql"
	}
	if strings.ContainsAny(ext, "/\\\x00") || ext == "." {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid scratch extension %q", ext))
	}
	path, err := s.ws.NewScratch(ext)
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.NewScratchResponse{Path: path}), nil
}

func (s *Service) ListScratches(context.Context, *connect.Request[quarryv1.ListScratchesRequest]) (*connect.Response[quarryv1.ListScratchesResponse], error) {
	files, err := s.ws.Scratches()
	if err != nil {
		return nil, fileError(err)
	}
	res := &quarryv1.ListScratchesResponse{Files: make([]*quarryv1.FileInfo, len(files))}
	for i, f := range files {
		res.Files[i] = &quarryv1.FileInfo{Path: f.Path, Name: f.Name, Modified: timestamppb.New(f.Modified), Size: f.Size}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) DeleteScratch(_ context.Context, req *connect.Request[quarryv1.DeleteScratchRequest]) (*connect.Response[quarryv1.DeleteScratchResponse], error) {
	if err := checkPath(req.Msg.Path); err != nil {
		return nil, err
	}
	if err := s.ws.DeleteScratch(req.Msg.Path); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.DeleteScratchResponse{}), nil
}

func (s *Service) LoadSession(context.Context, *connect.Request[quarryv1.LoadSessionRequest]) (*connect.Response[quarryv1.LoadSessionResponse], error) {
	sess, err := s.ws.LoadSession()
	if err != nil {
		return nil, fileError(err)
	}
	pb := &quarryv1.Session{
		Tabs:       make([]*quarryv1.TabState, len(sess.Tabs)),
		Active:     int32(sess.Active),
		LayoutJson: sess.Layout,
	}
	for i, t := range sess.Tabs {
		pt := &quarryv1.TabState{
			Path:    t.Path,
			Title:   t.Title,
			Cursor:  int32(t.Cursor),
			Unsaved: t.Unsaved,
			Dirty:   t.Dirty,
			Scope:   scopeProto(t.Scope),
		}
		if t.Conn != uuid.Nil() {
			pt.ConnId = t.Conn.String()
		}
		pb.Tabs[i] = pt
	}
	return connect.NewResponse(&quarryv1.LoadSessionResponse{Session: pb}), nil
}

func (s *Service) SaveSession(_ context.Context, req *connect.Request[quarryv1.SaveSessionRequest]) (*connect.Response[quarryv1.SaveSessionResponse], error) {
	pb := req.Msg.Session
	if pb == nil {
		pb = &quarryv1.Session{}
	}
	sess := ws.Session{
		Tabs:   make([]ws.TabState, len(pb.Tabs)),
		Active: int(pb.Active),
		Layout: pb.LayoutJson,
	}
	for i, t := range pb.Tabs {
		var id uuid.UUID
		if t.ConnId != "" {
			var err error
			if id, err = parseConn(t.ConnId); err != nil {
				return nil, err
			}
		}
		sess.Tabs[i] = ws.TabState{
			Path:    t.Path,
			Title:   t.Title,
			Conn:    id,
			Scope:   service.ScopeOf(t.Scope),
			Cursor:  int(t.Cursor),
			Unsaved: t.Unsaved,
			Dirty:   t.Dirty,
		}
	}
	if err := s.ws.SaveSession(sess); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.SaveSessionResponse{}), nil
}

func (s *Service) History(_ context.Context, req *connect.Request[quarryv1.HistoryRequest]) (*connect.Response[quarryv1.HistoryResponse], error) {
	id, err := parseConn(req.Msg.ConnId)
	if err != nil {
		return nil, err
	}
	limit := int(req.Msg.Limit)
	if limit < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("limit must not be negative"))
	}
	if limit == 0 {
		limit = defaultHistoryLimit
	}
	entries, err := s.ws.History(id, req.Msg.Query, limit)
	if err != nil {
		return nil, fileError(err)
	}
	res := &quarryv1.HistoryResponse{Entries: make([]*quarryv1.HistoryEntry, len(entries))}
	for i, e := range entries {
		res.Entries[i] = &quarryv1.HistoryEntry{
			At:         timestamppb.New(e.At),
			ConnId:     e.Conn.String(),
			Scope:      scopeProto(e.Scope),
			Text:       e.Text,
			DurationMs: e.Duration.Milliseconds(),
			Rows:       e.Rows,
			Error:      e.Err,
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) ClearHistory(_ context.Context, req *connect.Request[quarryv1.ClearHistoryRequest]) (*connect.Response[quarryv1.ClearHistoryResponse], error) {
	id, err := parseConn(req.Msg.ConnId)
	if err != nil {
		return nil, err
	}
	if err := s.ws.ClearHistory(id); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.ClearHistoryResponse{}), nil
}

var errNotObject = errors.New("settings must be a JSON object")

// GetSettings returns the settings file as is, "{}" when there is none. A
// file that is not a JSON object (as after a bad hand edit) fails with
// FailedPrecondition and is left untouched.
func (s *Service) GetSettings(context.Context, *connect.Request[quarryv1.GetSettingsRequest]) (*connect.Response[quarryv1.GetSettingsResponse], error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	v, err := s.readSettings()
	if errors.Is(err, fs.ErrNotExist) {
		return connect.NewResponse(&quarryv1.GetSettingsResponse{Json: "{}"}), nil
	}
	if errors.Is(err, errNotObject) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: %w", s.settingsPath, err))
	}
	if err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.GetSettingsResponse{Json: string(v)}), nil
}

// SaveSettings replaces the settings file with the given JSON object,
// indented for hand editing. An existing file that is not a JSON object is
// first kept aside as settings.json.corrupt-<time>.
func (s *Service) SaveSettings(_ context.Context, req *connect.Request[quarryv1.SaveSettingsRequest]) (*connect.Response[quarryv1.SaveSettingsResponse], error) {
	v := jsontext.Value(req.Msg.Json)
	if !v.IsValid() || v.Kind() != '{' {
		return nil, connect.NewError(connect.CodeInvalidArgument, errNotObject)
	}
	v = v.Clone()
	if err := v.Indent(jsontext.WithIndent("  ")); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	v = append(v, '\n')

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if _, err := s.readSettings(); errors.Is(err, errNotObject) {
		dst := fmt.Sprintf("%s.corrupt-%s", s.settingsPath, time.Now().Format("20060102-150405.000"))
		if err := os.Rename(s.settingsPath, dst); err != nil {
			return nil, fileError(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.settingsPath), 0o700); err != nil {
		return nil, fileError(err)
	}
	if err := ws.WriteAtomic(s.settingsPath, v, 0o600); err != nil {
		return nil, fileError(err)
	}
	return connect.NewResponse(&quarryv1.SaveSettingsResponse{}), nil
}

// readSettings reads the settings file, failing with errNotObject if it is
// not a JSON object. A UTF-8 BOM left by an editor is dropped.
func (s *Service) readSettings() (jsontext.Value, error) {
	b, err := os.ReadFile(s.settingsPath)
	if err != nil {
		return nil, err
	}
	v := jsontext.Value(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	if !v.IsValid() || v.Kind() != '{' {
		return nil, errNotObject
	}
	return v, nil
}

// checkPath rejects empty and relative paths: the backend's working
// directory means nothing to the frontend.
func checkPath(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("path %q is not absolute", path))
	}
	return nil
}

func parseConn(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid connection id %q", s))
	}
	return id, nil
}

// scopeProto is service.ScopeProto, but nil for the zero scope.
func scopeProto(sc driver.Scope) *quarryv1.Scope {
	if sc == (driver.Scope{}) {
		return nil
	}
	return service.ScopeProto(sc)
}

// fileError maps a file system error to a Connect error.
func fileError(err error) error {
	var code connect.Code
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.Is(err, ws.ErrTooLarge), errors.Is(err, ws.ErrBinary), errors.Is(err, ws.ErrNotScratch):
		code = connect.CodeInvalidArgument
	case errors.Is(err, fs.ErrNotExist):
		code = connect.CodeNotFound
	case errors.Is(err, fs.ErrPermission):
		code = connect.CodePermissionDenied
	case errors.As(err, &pe), errors.As(err, &le):
		code = connect.CodeUnavailable
	default:
		code = connect.CodeInternal
	}
	return connect.NewError(code, err)
}
