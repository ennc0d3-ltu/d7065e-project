// Package health is the shared health library (NFR8, S4): every process
// exposes /healthz, /readyz and a heartbeat through it.
package health

import (
	"net/http"
	"sync"
	"time"
)

// Health tracks liveness, readiness and the last heartbeat of one process.
type Health struct {
	name string
	now  func() time.Time

	mu    sync.Mutex
	ready bool
	last  time.Time
}

// New returns a Health for the named process. It is live but not ready.
func New(name string) *Health {
	return &Health{name: name, now: time.Now}
}

// Name returns the process name.
func (h *Health) Name() string { return h.name }

// SetReady marks the process ready (registered, dependencies reachable).
func (h *Health) SetReady(ready bool) {
	h.mu.Lock()
	h.ready = ready
	h.mu.Unlock()
}

// Beat records a heartbeat now.
func (h *Health) Beat() {
	h.mu.Lock()
	h.last = h.now()
	h.mu.Unlock()
}

// LastBeat returns the time of the last heartbeat (zero if none).
func (h *Health) LastBeat() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

// Stale reports whether no heartbeat arrived within maxAge.
func (h *Health) Stale(maxAge time.Duration) bool {
	last := h.LastBeat()
	return last.IsZero() || h.now().Sub(last) > maxAge
}

// Handler serves /healthz (always 200 while the process runs) and
// /readyz (200 only when ready).
func (h *Health) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		h.mu.Lock()
		ready := h.ready
		h.mu.Unlock()
		if !ready {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	return mux
}
