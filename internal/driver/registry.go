package driver

import "sync"

var (
	regMu    sync.RWMutex
	registry = map[Kind]Driver{}
)

// Register makes a driver available by its Kind. It is called from main for
// each compiled-in backend.
func Register(d Driver) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[d.Kind()] = d
}

// Lookup returns the driver registered for k.
func Lookup(k Kind) (Driver, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := registry[k]
	return d, ok
}
