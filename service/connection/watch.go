package connection

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	"connectrpc.com/connect"

	quarryv1 "github.com/gxd3v/quarry/gen/quarry/v1"
)

// watcher coalesces status changes for one WatchStatus stream so a slow
// client never blocks the manager: only the latest status per id is kept.
type watcher struct {
	notify chan struct{} // capacity 1

	mu      sync.Mutex
	changed map[string]*quarryv1.ConnectionStatus
	deleted map[string]bool
}

func newWatcher() *watcher {
	return &watcher{
		notify:  make(chan struct{}, 1),
		changed: map[string]*quarryv1.ConnectionStatus{},
		deleted: map[string]bool{},
	}
}

// put records a status, or a deletion when s is nil.
func (w *watcher) put(id string, s *quarryv1.ConnectionStatus) {
	w.mu.Lock()
	if s == nil {
		delete(w.changed, id)
		w.deleted[id] = true
	} else {
		delete(w.deleted, id)
		w.changed[id] = s
	}
	w.mu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// take returns the pending changes, or nil when there are none.
func (w *watcher) take() *quarryv1.WatchStatusResponse {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.changed) == 0 && len(w.deleted) == 0 {
		return nil
	}
	msg := &quarryv1.WatchStatusResponse{
		Statuses: sortStatuses(slices.Collect(maps.Values(w.changed))),
		Deleted:  slices.Sorted(maps.Keys(w.deleted)),
	}
	clear(w.changed)
	clear(w.deleted)
	return msg
}

func sortStatuses(s []*quarryv1.ConnectionStatus) []*quarryv1.ConnectionStatus {
	slices.SortFunc(s, func(a, b *quarryv1.ConnectionStatus) int { return cmp.Compare(a.Id, b.Id) })
	return s
}

// watch sends every status, then each change, until ctx is done or the
// manager closes.
func (m *Manager) watch(ctx context.Context, send func(*quarryv1.WatchStatusResponse) error) error {
	w := newWatcher()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return connect.NewError(connect.CodeUnavailable, errors.New("connection manager is closed"))
	}
	first := &quarryv1.WatchStatusResponse{Statuses: make([]*quarryv1.ConnectionStatus, 0, len(m.states))}
	for _, st := range m.states {
		first.Statuses = append(first.Statuses, st.status)
	}
	sortStatuses(first.Statuses)
	m.watchers[w] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.watchers, w)
		m.mu.Unlock()
	}()

	if err := send(first); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.ctx.Done():
			return nil
		case <-w.notify:
			if msg := w.take(); msg != nil {
				if err := send(msg); err != nil {
					return err
				}
			}
		}
	}
}
