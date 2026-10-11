package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dbwriter"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// ErrUnavailable means the database is not reachable (yet). The writer keeps
// its buffer and tries again; nothing upstream depends on it.
var ErrUnavailable = errors.New("store: database unavailable")

// Opener connects to the database (and migrates it).
type Opener func(ctx context.Context) (dbwriter.Store, error)

// Reconnecting lets the db-writer process start, and keep running, while the
// database is down: it connects on first use, returns ErrUnavailable while it
// cannot, and does not retry more often than minWait (NFR4, NFR6).
type Reconnecting struct {
	open    Opener
	minWait time.Duration
	now     func() time.Time

	mu      sync.Mutex
	inner   dbwriter.Store
	lastTry time.Time
}

// NewReconnecting returns a store that connects lazily through open.
func NewReconnecting(open Opener, minWait time.Duration) *Reconnecting {
	return &Reconnecting{open: open, minWait: minWait, now: time.Now}
}

func (r *Reconnecting) get(ctx context.Context) (dbwriter.Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inner != nil {
		return r.inner, nil
	}
	if !r.lastTry.IsZero() && r.now().Sub(r.lastTry) < r.minWait {
		return nil, ErrUnavailable
	}
	r.lastTry = r.now()
	inner, err := r.open(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	r.inner = inner
	return inner, nil
}

// InsertReadings implements dbwriter.Store.
func (r *Reconnecting) InsertReadings(ctx context.Context, rs []gateway.Reading) error {
	s, err := r.get(ctx)
	if err != nil {
		return err
	}
	return s.InsertReadings(ctx, rs)
}

// InsertDecisions implements dbwriter.Store.
func (r *Reconnecting) InsertDecisions(ctx context.Context, ds []proto.Decision) error {
	s, err := r.get(ctx)
	if err != nil {
		return err
	}
	return s.InsertDecisions(ctx, ds)
}

// InsertCommands implements dbwriter.Store.
func (r *Reconnecting) InsertCommands(ctx context.Context, cs []proto.Command) error {
	s, err := r.get(ctx)
	if err != nil {
		return err
	}
	return s.InsertCommands(ctx, cs)
}

// InsertDeadLetters implements dbwriter.Store.
func (r *Reconnecting) InsertDeadLetters(ctx context.Context, ds []proto.DeadLetter) error {
	s, err := r.get(ctx)
	if err != nil {
		return err
	}
	return s.InsertDeadLetters(ctx, ds)
}

// InsertDispatches implements dbwriter.Store.
func (r *Reconnecting) InsertDispatches(ctx context.Context, ds []proto.Dispatch, acks []proto.DispatchAck) error {
	s, err := r.get(ctx)
	if err != nil {
		return err
	}
	return s.InsertDispatches(ctx, ds, acks)
}

// InsertHeartbeats implements dbwriter.Store.
func (r *Reconnecting) InsertHeartbeats(ctx context.Context, hs []proto.Heartbeat) error {
	s, err := r.get(ctx)
	if err != nil {
		return err
	}
	return s.InsertHeartbeats(ctx, hs)
}
