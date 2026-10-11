package detectsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/registry"
)

// Applier takes a validated set of thresholds and model into use.
type Applier interface {
	Apply(th detector.Thresholds, model *detector.Model)
}

// Loader keeps the detector's thresholds and model in step with the registry's
// active bundle (decision D15). The detector never depends on the registry to
// run: it starts from its built-in defaults or its local cache, polls with a
// timeout, validates what it gets (the hard limits are checked here again, in
// the detector's own process), and keeps the last good bundle on any error.
type Loader struct {
	URL       string // registry base URL
	CachePath string // where the last good bundle is kept; "" disables the cache
	Applier   Applier
	Client    *http.Client
	Log       *slog.Logger

	mu      sync.Mutex
	version int
	lastErr string
}

// Version is the bundle version in use (0: built-in defaults).
func (l *Loader) Version() int { l.mu.Lock(); defer l.mu.Unlock(); return l.version }

// LastError is the reason the latest poll or bundle was not used, or "".
func (l *Loader) LastError() string { l.mu.Lock(); defer l.mu.Unlock(); return l.lastErr }

func (l *Loader) logger() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (l *Loader) fail(err error) {
	l.mu.Lock()
	l.lastErr = err.Error()
	l.mu.Unlock()
	l.logger().Warn("registry bundle not used; keeping the current one", "err", err)
}

// apply validates a bundle and, only if it passes, puts it into use.
func (l *Loader) apply(b registry.Bundle) error {
	if b.Version == l.Version() {
		return nil
	}
	if err := b.Validate(); err != nil {
		return err
	}
	var model *detector.Model
	if len(b.Model) > 0 {
		model = new(detector.Model)
		if err := json.Unmarshal(b.Model, model); err != nil {
			return fmt.Errorf("model does not load: %w", err)
		}
	}
	l.Applier.Apply(b.Thresholds, model)
	l.mu.Lock()
	l.version, l.lastErr = b.Version, ""
	l.mu.Unlock()
	l.logger().Info("registry bundle applied", "version", b.Version, "note", b.Note, "promoted_by", b.PromotedBy)
	return nil
}

// LoadCache starts from the last good bundle kept on disk; false if there is
// none or it is unusable.
func (l *Loader) LoadCache() bool {
	if l.CachePath == "" {
		return false
	}
	raw, err := os.ReadFile(l.CachePath)
	if err != nil {
		return false
	}
	var b registry.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		l.fail(fmt.Errorf("cache unreadable: %w", err))
		return false
	}
	if err := l.apply(b); err != nil {
		l.fail(fmt.Errorf("cache not valid: %w", err))
		return false
	}
	return true
}

// Poll asks the registry once. Any failure leaves the current bundle in use.
func (l *Loader) Poll(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL+"/active", nil)
	if err != nil {
		l.fail(err)
		return
	}
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		l.fail(err)
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return // nothing active: keep what we have
	case resp.StatusCode != http.StatusOK:
		l.fail(fmt.Errorf("registry answered %s", resp.Status))
		return
	}
	var b registry.Bundle
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&b); err != nil {
		l.fail(fmt.Errorf("bad bundle: %w", err))
		return
	}
	fresh := b.Version != l.Version()
	if err := l.apply(b); err != nil {
		l.fail(err)
		return
	}
	if fresh {
		l.saveCache(b)
	}
}

func (l *Loader) saveCache(b registry.Bundle) {
	if l.CachePath == "" {
		return
	}
	raw, err := json.Marshal(b)
	if err == nil {
		err = writeFile(l.CachePath, raw)
	}
	if err != nil {
		l.logger().Warn("cannot cache the bundle", "err", err)
	}
}

// Run polls every interval until ctx ends.
func (l *Loader) Run(ctx context.Context, every time.Duration) {
	l.Poll(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Poll(ctx)
		}
	}
}

// writeFile replaces a file atomically so a crash never leaves half a cache.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
